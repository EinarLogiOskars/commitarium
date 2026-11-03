//! Backups (ADR-011): every volume of the stack, and the files a side keeps
//! of its own, in a folder of archives with a checked manifest. The app on
//! this computer and a server run this same code, so either restores the
//! other's backups (SERVER_COMPLETE_PLAN.md, S1). A restore reads an archive
//! in full before it replaces anything, and keeps a safety copy to roll back
//! to if it fails part way.

use crate::{agents, bootstrap, docker};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::borrow::Cow;
use std::collections::BTreeMap;
use std::fs::{self, File};
use std::io::Read;
use std::path::{Component, Path, PathBuf};
use std::process::Stdio;

/// What differs between the app on this computer and a server.
pub struct Context<'a> {
    /// Where this side keeps the files a backup carries besides the volumes.
    pub state_dir: PathBuf,
    /// Those files, by name.
    pub state_files: &'a [&'a str],
    /// Says what the backup is doing, for whoever is watching.
    pub progress: &'a (dyn Fn(&str) + Sync),
    /// Brings the stack back up after the backup stopped it.
    pub start_stack: &'a (dyn Fn() -> Result<(), String> + Sync),
}

const BACKUP_FORMAT: &str = "commitarium-backup";
const BACKUP_FORMAT_VERSION: u32 = 1;

/// Unpacked beside the old data, inside the volume, so nothing is deleted
/// until the archive has been read in full.
const RESTORE_STAGING: &str = ".commitarium-restore";

const CORE_COMPONENTS: &[BackupComponent] = &[
    BackupComponent {
        name: Cow::Borrowed("coordinator"),
        service: Cow::Borrowed("coordinator"),
        source: "/var/lib/commitarium",
        owner: "65532:65532",
        required: true,
        leave_out: &[],
    },
    BackupComponent {
        name: Cow::Borrowed("forgejo"),
        service: Cow::Borrowed("forgejo"),
        source: "/var/lib/gitea",
        owner: "1000:1000",
        required: true,
        leave_out: &[],
    },
    BackupComponent {
        name: Cow::Borrowed("workspaces"),
        service: Cow::Borrowed("coordinator"),
        source: "/workspaces",
        owner: "65532:65532",
        required: true,
        // Dependencies are installed again; they were most of a backup.
        leave_out: &["node_modules"],
    },
    BackupComponent {
        name: Cow::Borrowed("toolchains"),
        service: Cow::Borrowed("coordinator"),
        source: "/var/lib/commitarium-toolchains",
        owner: "65532:65532",
        required: true,
        leave_out: &[],
    },
    BackupComponent {
        name: Cow::Borrowed("simulated-worker-journal"),
        service: Cow::Borrowed("simulated-codex-worker"),
        source: "/var/lib/commitarium-worker",
        owner: "65532:65532",
        required: false,
        leave_out: &[],
    },
];

/// Journals of the fixed pre-agent workers in older backups. The lead
/// journals restore into the agents migrated from them; the reviewer journals
/// have no worker any more and are skipped (ADR-016).
const LEGACY_JOURNALS: &[(&str, &str)] = &[
    ("codex-worker-journal", "agent-codex-worker"),
    ("claude-worker-journal", "agent-claude-worker"),
    ("codex-reviewer-worker-journal", "codex-reviewer-worker"),
    ("claude-reviewer-worker-journal", "claude-reviewer-worker"),
];

#[derive(Clone)]
struct BackupComponent {
    name: Cow<'static, str>,
    service: Cow<'static, str>,
    source: &'static str,
    owner: &'static str,
    required: bool,
    /// What is left out wherever it appears: what can be made again.
    leave_out: &'static [&'static str],
}

fn journal_component(name: String, service: String) -> BackupComponent {
    BackupComponent {
        name: Cow::Owned(name),
        service: Cow::Owned(service),
        source: "/var/lib/commitarium-worker",
        owner: "65532:65532",
        required: false,
        leave_out: &[],
    }
}

/// Every component a backup of this installation contains: the core state
/// and each agent worker's journal.
fn backup_components(agents: &[agents::Agent]) -> Vec<BackupComponent> {
    let mut components = CORE_COMPONENTS.to_vec();
    components.extend(agents.iter().map(|agent| {
        let service = agents::worker_service(&agent.id);
        journal_component(format!("{service}-journal"), service)
    }));
    components
}

/// Resolve a component named in a backup manifest.
fn backup_component(name: &str) -> Option<BackupComponent> {
    if let Some(core) = CORE_COMPONENTS.iter().find(|core| core.name == name) {
        return Some(core.clone());
    }
    if let Some((legacy, service)) = LEGACY_JOURNALS.iter().find(|(legacy, _)| *legacy == name) {
        return Some(journal_component(legacy.to_string(), service.to_string()));
    }
    let agent_id = name
        .strip_prefix("agent-")?
        .strip_suffix("-worker-journal")?;
    agents::valid_id(agent_id).then(|| {
        let service = agents::worker_service(agent_id);
        journal_component(name.to_string(), service)
    })
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct BackupManifest {
    format: String,
    format_version: u32,
    app_version: String,
    created_at_unix_seconds: u64,
    credentials_included: bool,
    components: Vec<String>,
    sha256: BTreeMap<String, String>,
}

#[derive(Clone, Debug, Serialize)]
pub struct BackupResult {
    path: String,
    format_version: u32,
    components: Vec<String>,
    credentials_included: bool,
}

#[derive(Clone, Debug, Serialize)]
pub struct BackupInspection {
    path: String,
    format_version: u32,
    app_version: String,
    created_at_unix_seconds: u64,
    components: Vec<String>,
    credentials_included: bool,
}

pub fn create(ctx: &Context, destination: &Path) -> Result<BackupResult, String> {
    validate_destination(destination)?;
    if destination.exists() {
        return Err("backup destination already exists".to_string());
    }
    let parent = destination
        .parent()
        .ok_or_else(|| "backup destination has no parent directory".to_string())?;
    fs::create_dir_all(parent).map_err(|e| format!("create backup parent: {e}"))?;
    let staging = tempfile::Builder::new()
        .prefix(".commitarium-backup-")
        .tempdir_in(parent)
        .map_err(|e| format!("create backup staging directory: {e}"))?;

    let was_running = stack_has_running_services()?;
    report_backup_step(ctx, "Stopping services…");
    docker::compose(docker::PROVIDER_PROFILES, &["stop"])?;
    let result = export_backup(ctx, staging.path(), "Backing up");
    let restart = if was_running {
        report_backup_step(ctx, "Starting services…");
        (ctx.start_stack)()
    } else {
        Ok(())
    };
    let manifest = match (result, restart) {
        (Ok(manifest), Ok(())) => manifest,
        (Err(backup), Ok(())) => return Err(backup),
        (Ok(_), Err(restart)) => {
            return Err(format!(
                "backup was created but the stack did not restart: {restart}"
            ))
        }
        (Err(backup), Err(restart)) => {
            return Err(format!(
                "backup failed ({backup}) and the stack did not restart ({restart})"
            ))
        }
    };
    let components = manifest.components.clone();
    let persisted = staging.keep();
    fs::rename(&persisted, destination)
        .map_err(|e| format!("publish backup at {}: {e}", destination.display()))?;
    Ok(BackupResult {
        path: destination.to_string_lossy().into_owned(),
        format_version: BACKUP_FORMAT_VERSION,
        components,
        credentials_included: false,
    })
}

fn report_backup_step(ctx: &Context, step: &str) {
    (ctx.progress)(step);
}

fn report_component(ctx: &Context, action: &str, name: &str, index: usize, total: usize) {
    let what = match name {
        "coordinator" => "your records",
        "forgejo" => "repositories",
        "workspaces" => "agents' working copies",
        "toolchains" => "toolchains and chats",
        _ => "agent history",
    };
    let position = index + 1;
    report_backup_step(
        ctx,
        &format!("{action} {what}, step {position} of {total}…"),
    );
}

fn export_backup(
    ctx: &Context,
    destination: &Path,
    action: &str,
) -> Result<BackupManifest, String> {
    let coordinator_id = container_id("coordinator")?
        .ok_or_else(|| "the Commitarium stack has not been created yet".to_string())?;
    let helper_image = container_image(&coordinator_id)?;
    let mut components = Vec::new();
    let mut hashes = BTreeMap::new();

    let agents = agents::current().unwrap_or_default();
    let all = backup_components(&agents);
    let deployments = deployment_volumes()?;
    // Each deployment's data is a step too.
    let total = all.len() + deployments.len();
    for (index, component) in all.iter().enumerate() {
        report_component(ctx, action, &component.name, index, total);
        let Some(container_id) = container_id(&component.service)? else {
            if component.required {
                return Err(format!(
                    "required {} container does not exist",
                    component.service
                ));
            }
            continue;
        };
        let filename = format!("{}.tar.gz", component.name);
        export_component(
            &helper_image,
            &container_id,
            component.source,
            component.leave_out,
            &destination.join(&filename),
        )?;
        hashes.insert(
            filename,
            sha256_file(&destination.join(format!("{}.tar.gz", component.name)))?,
        );
        components.push(component.name.to_string());
    }

    // Deployments' data: what visitors saved (PUBLISHING_PLAN.md).
    for (index, volume) in deployments.into_iter().enumerate() {
        let name = format!("{DEPLOYMENT_PREFIX}{volume}");
        let position = all.len() + index + 1;
        (ctx.progress)(&format!(
            "{action} a deployed app's data, step {position} of {total}…"
        ));
        let filename = format!("{name}.tar.gz");
        let archive = destination.join(&filename);
        // Paused, its files hold still while they are copied.
        let project = volume_project(&volume);
        set_paused(project, true);
        let exported = export_volume(&helper_image, &volume, &archive);
        set_paused(project, false);
        exported?;
        hashes.insert(filename, sha256_file(&archive)?);
        components.push(name);
    }

    let state_dir = destination.join("app-state");
    fs::create_dir_all(&state_dir).map_err(|e| format!("create app-state backup: {e}"))?;
    let app_data = ctx.state_dir.clone();
    for name in ctx.state_files {
        let source = app_data.join(name);
        if !source.is_file() {
            continue;
        }
        let relative = format!("app-state/{name}");
        fs::copy(&source, destination.join(&relative))
            .map_err(|e| format!("back up {name}: {e}"))?;
        hashes.insert(relative.clone(), sha256_file(&destination.join(relative))?);
    }

    let manifest = BackupManifest {
        format: BACKUP_FORMAT.to_string(),
        format_version: BACKUP_FORMAT_VERSION,
        app_version: env!("CARGO_PKG_VERSION").to_string(),
        created_at_unix_seconds: std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map_err(|_| "system time is before Unix epoch".to_string())?
            .as_secs(),
        credentials_included: false,
        components,
        sha256: hashes,
    };
    write_json_atomic(&destination.join("manifest.json"), &manifest)?;
    Ok(manifest)
}

pub fn restore(ctx: &Context, source: &Path) -> Result<BackupResult, String> {
    let manifest = read_backup_manifest(source, ctx.state_files)?;
    let compose_file = docker::compose_file()?;
    let agents = agents::current().unwrap_or_default();
    bootstrap::prepare_transport_secrets(&compose_file)?;
    bootstrap::prepare_agent_worker_tokens(&compose_file, &agents)?;
    bootstrap::prepare_forgejo_secret_mounts(&compose_file, &agents)?;
    if docker::release_mode() {
        docker::compose_base(docker::PROVIDER_PROFILES, &["pull", "--policy", "missing"])?;
    }
    let mut create_args = vec!["create"];
    if docker::release_mode() {
        create_args.push("--no-build");
    }
    report_backup_step(ctx, "Stopping services…");
    docker::compose(docker::PROVIDER_PROFILES, &create_args)?;
    docker::compose(docker::PROVIDER_PROFILES, &["stop"])?;

    let coordinator_id = container_id("coordinator")?
        .ok_or_else(|| "could not create the coordinator restore container".to_string())?;
    let helper_image = container_image(&coordinator_id)?;
    let recovery = tempfile::Builder::new()
        .prefix("restore-rollback-")
        .tempdir_in(ctx.state_dir.clone())
        .map_err(|e| format!("create restore rollback directory: {e}"))?;
    let recovery_manifest = export_backup(ctx, recovery.path(), "Saving a safety copy of")?;

    if let Err(restore_error) = apply_backup(ctx, source, &manifest, &helper_image) {
        let rollback = apply_backup(ctx, recovery.path(), &recovery_manifest, &helper_image);
        report_backup_step(ctx, "Starting services…");
        let restart = (ctx.start_stack)();
        return match (rollback, restart) {
            (Ok(()), Ok(())) => Err(format!(
                "restore failed and the previous installation was restored: {restore_error}"
            )),
            (rollback, restart) => {
                // Without this copy a failed rollback would lose the only
                // record of what was here.
                let kept = recovery.keep();
                Err(format!(
                    "restore failed ({restore_error}); automatic rollback was incomplete (rollback: {}; restart: {}). A copy of the previous installation is at {}",
                    rollback.err().unwrap_or_else(|| "ok".to_string()),
                    restart.err().unwrap_or_else(|| "ok".to_string()),
                    kept.display()
                ))
            }
        };
    }
    report_backup_step(ctx, "Starting services…");
    (ctx.start_stack)()?;
    Ok(BackupResult {
        path: source.to_string_lossy().into_owned(),
        format_version: manifest.format_version,
        components: manifest.components,
        credentials_included: manifest.credentials_included,
    })
}

fn apply_backup(
    ctx: &Context,
    source: &Path,
    manifest: &BackupManifest,
    helper_image: &str,
) -> Result<(), String> {
    for (index, name) in manifest.components.iter().enumerate() {
        if let Some(volume) = deployment_volume(name) {
            (ctx.progress)("Restoring a deployment's data…");
            restore_volume(
                helper_image,
                &volume,
                &source.join(format!("{name}.tar.gz")),
            )?;
            continue;
        }
        let component = backup_component(name)
            .ok_or_else(|| format!("backup contains unknown component {name}"))?;
        report_component(ctx, "Restoring", name, index, manifest.components.len());
        // A journal whose agent is not configured here has no worker to
        // restore into; its work orders can no longer resume anyway.
        let Some(container_id) = container_id(&component.service)? else {
            if component.required {
                return Err(format!(
                    "could not create restore container for {}",
                    component.service
                ));
            }
            continue;
        };
        restore_component(
            helper_image,
            &container_id,
            &component,
            &source.join(format!("{}.tar.gz", component.name)),
        )?;
    }
    restore_app_state(ctx, source)?;
    Ok(())
}

fn read_backup_manifest(source: &Path, state_files: &[&str]) -> Result<BackupManifest, String> {
    validate_destination(source)?;
    let encoded =
        fs::read(source.join("manifest.json")).map_err(|e| format!("read backup manifest: {e}"))?;
    let manifest: BackupManifest =
        serde_json::from_slice(&encoded).map_err(|e| format!("parse backup manifest: {e}"))?;
    if manifest.format != BACKUP_FORMAT || manifest.format_version != BACKUP_FORMAT_VERSION {
        return Err("backup format or version is not supported".to_string());
    }
    if manifest.credentials_included {
        return Err("credential-bearing backups are not supported".to_string());
    }
    let mut seen = std::collections::BTreeSet::new();
    for name in &manifest.components {
        if backup_component(name).is_none() && deployment_volume(name).is_none() {
            return Err(format!("backup contains unknown component {name}"));
        }
        if !seen.insert(name.as_str()) {
            return Err(format!("backup contains duplicate component {name}"));
        }
        let archive = format!("{name}.tar.gz");
        if !manifest.sha256.contains_key(&archive) {
            return Err(format!("backup has no checksum for {archive}"));
        }
    }
    for required in CORE_COMPONENTS
        .iter()
        .filter(|component| component.required)
    {
        if !seen.contains(required.name.as_ref()) {
            return Err(format!(
                "backup is missing required component {}",
                required.name
            ));
        }
    }
    let mut allowed_files = seen
        .iter()
        .map(|name| format!("{name}.tar.gz"))
        .collect::<std::collections::BTreeSet<_>>();
    for name in state_files {
        let relative = format!("app-state/{name}");
        if source.join(&relative).is_file() && !manifest.sha256.contains_key(&relative) {
            return Err(format!("backup has no checksum for {relative}"));
        }
        allowed_files.insert(relative);
    }
    for (relative, expected) in &manifest.sha256 {
        if !allowed_files.contains(relative) {
            return Err(format!("backup manifest contains unknown file {relative}"));
        }
        let relative_path = safe_relative_path(relative)?;
        let actual = sha256_file(&source.join(relative_path))?;
        if &actual != expected {
            return Err(format!("backup checksum failed for {relative}"));
        }
    }
    Ok(manifest)
}

fn restore_app_state(ctx: &Context, source: &Path) -> Result<(), String> {
    let app_data = ctx.state_dir.clone();
    for name in ctx.state_files {
        let backup = source.join("app-state").join(name);
        if backup.is_file() {
            fs::copy(&backup, app_data.join(name)).map_err(|e| format!("restore {name}: {e}"))?;
        } else {
            match fs::remove_file(app_data.join(name)) {
                Ok(()) => {}
                Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
                Err(error) => return Err(format!("clear {name} before restore: {error}")),
            }
        }
    }
    Ok(())
}

fn export_component(
    image: &str,
    container_id: &str,
    source: &str,
    leave_out: &[&str],
    destination: &Path,
) -> Result<(), String> {
    let output =
        File::create(destination).map_err(|e| format!("create {}: {e}", destination.display()))?;
    let status = docker::docker_command()
        .args(["run", "--rm", "--user", "0", "--volumes-from", container_id])
        .args(["--entrypoint", "/bin/tar", image, "-C", source])
        .args(leave_out.iter().flat_map(|name| ["--exclude", name]))
        .args(["-czf", "-", "."])
        .stdout(Stdio::from(output))
        .stderr(Stdio::piped())
        .output()
        .map_err(|e| format!("archive {source}: {e}"))?;
    if !status.status.success() {
        return Err(format!(
            "archive {source}: {}",
            String::from_utf8_lossy(&status.stderr).trim()
        ));
    }
    Ok(())
}

fn restore_component(
    image: &str,
    container_id: &str,
    component: &BackupComponent,
    archive: &Path,
) -> Result<(), String> {
    let input = File::open(archive).map_err(|e| format!("open {}: {e}", archive.display()))?;
    let output = docker::docker_command()
        .args(restore_args(image, container_id, component))
        .stdin(Stdio::from(input))
        .output()
        .map_err(|e| format!("restore {}: {e}", component.name))?;
    if !output.status.success() {
        return Err(format!(
            "restore {}: {}",
            component.name,
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    Ok(())
}

/// `-i` is what hands the archive to the container. The volume is only
/// emptied once the whole archive has unpacked, so a bad read changes nothing.
fn restore_args(image: &str, container_id: &str, component: &BackupComponent) -> Vec<String> {
    let source = component.source;
    let owner = &component.owner;
    let staging = format!("{source}/{RESTORE_STAGING}");
    let script = format!(
        "rm -rf '{staging}' && mkdir '{staging}' && /bin/tar -C '{staging}' -xzf - \\
         && find '{source}' -mindepth 1 -maxdepth 1 ! -name '{RESTORE_STAGING}' -exec rm -rf -- {{}} + \\
         && find '{staging}' -mindepth 1 -maxdepth 1 -exec mv -- {{}} '{source}/' \\; \\
         && rmdir '{staging}' && chown -R '{owner}' '{source}'"
    );
    [
        "run",
        "--rm",
        "-i",
        "--user",
        "0",
        "--volumes-from",
        container_id,
        "--entrypoint",
        "/bin/sh",
        image,
        "-c",
        &script,
    ]
    .map(str::to_string)
    .to_vec()
}

/// The container of a Commitarium service, found by its Compose labels so a
/// service that is no longer defined simply has none.
fn container_id(service: &str) -> Result<Option<String>, String> {
    let output = docker::docker_command()
        .args(["ps", "--all", "--quiet", "--filter"])
        .arg(format!(
            "label=com.docker.compose.project={}",
            docker::PROJECT_NAME
        ))
        .arg("--filter")
        .arg(format!("label=com.docker.compose.service={service}"))
        .output()
        .map_err(|e| format!("find the {service} container: {e}"))?;
    if !output.status.success() {
        return Err(format!("could not find the {service} container"));
    }
    let id = String::from_utf8_lossy(&output.stdout)
        .lines()
        .next()
        .unwrap_or_default()
        .trim()
        .to_string();
    Ok((!id.is_empty()).then_some(id))
}

fn container_image(container_id: &str) -> Result<String, String> {
    let output = docker::docker_command()
        .args(["inspect", "--format", "{{.Config.Image}}", container_id])
        .output()
        .map_err(|e| format!("inspect backup helper image: {e}"))?;
    if !output.status.success() {
        return Err(format!(
            "inspect backup helper image: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    let image = String::from_utf8_lossy(&output.stdout).trim().to_string();
    if image.is_empty() {
        return Err("backup helper image is empty".to_string());
    }
    Ok(image)
}

fn stack_has_running_services() -> Result<bool, String> {
    let output = docker::compose(docker::PROVIDER_PROFILES, &["ps", "-q"])?;
    Ok(!output.trim().is_empty())
}

fn validate_destination(path: &Path) -> Result<(), String> {
    if !path.is_absolute() || path.as_os_str().is_empty() {
        return Err("backup path must be absolute".to_string());
    }
    Ok(())
}

fn safe_relative_path(value: &str) -> Result<PathBuf, String> {
    let path = Path::new(value);
    if path.is_absolute()
        || path.components().any(|component| {
            matches!(
                component,
                Component::ParentDir | Component::RootDir | Component::Prefix(_)
            )
        })
    {
        return Err("backup manifest contains an unsafe path".to_string());
    }
    Ok(path.to_path_buf())
}

fn sha256_file(path: &Path) -> Result<String, String> {
    let metadata =
        fs::symlink_metadata(path).map_err(|e| format!("inspect {}: {e}", path.display()))?;
    if !metadata.is_file() || metadata.file_type().is_symlink() {
        return Err(format!(
            "backup member {} is not a regular file",
            path.display()
        ));
    }
    let mut file = File::open(path).map_err(|e| format!("open {}: {e}", path.display()))?;
    let mut digest = Sha256::new();
    let mut buffer = [0_u8; 64 * 1024];
    loop {
        let read = file
            .read(&mut buffer)
            .map_err(|e| format!("read {}: {e}", path.display()))?;
        if read == 0 {
            break;
        }
        digest.update(&buffer[..read]);
    }
    Ok(format!("{:x}", digest.finalize()))
}

/// A deployment volume's component name: the volume's own, prefixed.
const DEPLOYMENT_PREFIX: &str = "deployment--";

/// The deployment volume a component names, if it is one.
fn deployment_volume(name: &str) -> Option<String> {
    let volume = name.strip_prefix(DEPLOYMENT_PREFIX)?;
    // A deployed app's, or authentik's (company sign-in): data people saved.
    let ours = (volume.starts_with(crate::preview::PREVIEW_TEMP_PREFIX)
        && volume.contains("-live."))
        || volume.starts_with(&format!("{}.", crate::authentik::PROJECT));
    let valid = ours
        && volume.len() <= 200
        && volume
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b));
    valid.then(|| volume.to_string())
}

/// The Compose project a deployment volume belongs to: its name before the
/// volume's own.
fn volume_project(volume: &str) -> &str {
    volume
        .rsplit_once('.')
        .map_or(volume, |(project, _)| project)
}

/// Every deployment's data volume.
fn deployment_volumes() -> Result<Vec<String>, String> {
    let output = docker::docker_command()
        .args(["volume", "ls", "--format", "{{.Name}}"])
        .output()
        .map_err(|e| format!("list volumes: {e}"))?;
    if !output.status.success() {
        return Err("could not list Docker volumes".into());
    }
    Ok(String::from_utf8_lossy(&output.stdout)
        .lines()
        .filter_map(|volume| deployment_volume(&format!("{DEPLOYMENT_PREFIX}{volume}")))
        .collect())
}

/// The containers of a Compose project.
fn project_containers(project: &str, running: bool) -> Vec<String> {
    let mut command = docker::docker_command();
    command.args(["ps", "--quiet", "--filter"]);
    command.arg(format!("label=com.docker.compose.project={project}"));
    if !running {
        command.arg("--all");
    }
    command
        .output()
        .map(|output| {
            String::from_utf8_lossy(&output.stdout)
                .split_whitespace()
                .map(str::to_string)
                .collect()
        })
        .unwrap_or_default()
}

fn set_paused(project: &str, paused: bool) {
    let containers = project_containers(project, true);
    if containers.is_empty() {
        return;
    }
    let _ = docker::docker_command()
        .arg(if paused { "pause" } else { "unpause" })
        .args(containers)
        .output();
}

fn export_volume(image: &str, volume: &str, destination: &Path) -> Result<(), String> {
    let output =
        File::create(destination).map_err(|e| format!("create {}: {e}", destination.display()))?;
    let status = docker::docker_command()
        .args(["run", "--rm", "--user", "0", "-v"])
        .arg(format!("{volume}:/data:ro"))
        .args([
            "--entrypoint",
            "/bin/tar",
            image,
            "-C",
            "/data",
            "-czf",
            "-",
            ".",
        ])
        .stdout(Stdio::from(output))
        .stderr(Stdio::piped())
        .output()
        .map_err(|e| format!("archive {volume}: {e}"))?;
    if !status.status.success() {
        return Err(format!(
            "archive {volume}: {}",
            String::from_utf8_lossy(&status.stderr).trim()
        ));
    }
    Ok(())
}

/// Restores a deployment's volume with its app stopped, creating the volume
/// for a deployment that has not run here yet. Unpacked first, beside the
/// old data, as the stack's volumes are.
fn restore_volume(image: &str, volume: &str, archive: &Path) -> Result<(), String> {
    let project = volume_project(volume);
    let containers = project_containers(project, true);
    if !containers.is_empty() {
        let _ = docker::docker_command()
            .arg("stop")
            .args(&containers)
            .output();
    }
    let exists = docker::docker_command()
        .args(["volume", "inspect", volume])
        .output()
        .is_ok_and(|output| output.status.success());
    if !exists {
        let name = volume.rsplit_once('.').map_or(volume, |(_, name)| name);
        let created = docker::docker_command()
            .args(["volume", "create", "--label"])
            .arg(format!("com.docker.compose.project={project}"))
            .arg("--label")
            .arg(format!("com.docker.compose.volume={name}"))
            .arg(volume)
            .output()
            .map_err(|e| format!("create {volume}: {e}"))?;
        if !created.status.success() {
            return Err(format!("could not create {volume}"));
        }
    }
    let input = File::open(archive).map_err(|e| format!("open {}: {e}", archive.display()))?;
    let staging = format!("/data/{RESTORE_STAGING}");
    let script = format!(
        "rm -rf '{staging}' && mkdir '{staging}' && /bin/tar -C '{staging}' -xzf - \
         && find /data -mindepth 1 -maxdepth 1 ! -name '{RESTORE_STAGING}' -exec rm -rf -- {{}} + \
         && find '{staging}' -mindepth 1 -maxdepth 1 -exec mv -- {{}} /data/ \\; \
         && rmdir '{staging}'"
    );
    let output = docker::docker_command()
        .args(["run", "--rm", "-i", "--user", "0", "-v"])
        .arg(format!("{volume}:/data"))
        .args(["--entrypoint", "/bin/sh", image, "-c", &script])
        .stdin(Stdio::from(input))
        .output()
        .map_err(|e| format!("restore {volume}: {e}"))?;
    if !containers.is_empty() {
        let _ = docker::docker_command()
            .arg("start")
            .args(&containers)
            .output();
    }
    if !output.status.success() {
        return Err(format!(
            "restore {volume}: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    Ok(())
}

/// What a backup holds, read and checked without restoring it.
pub fn inspect(source: &Path, state_files: &[&str]) -> Result<BackupInspection, String> {
    let manifest = read_backup_manifest(source, state_files)?;
    Ok(BackupInspection {
        path: source.to_string_lossy().into_owned(),
        format_version: manifest.format_version,
        app_version: manifest.app_version,
        created_at_unix_seconds: manifest.created_at_unix_seconds,
        components: manifest.components,
        credentials_included: manifest.credentials_included,
    })
}

fn write_json_atomic<T: Serialize>(path: &Path, value: &T) -> Result<(), String> {
    let temporary = path.with_extension("json.tmp");
    let encoded = serde_json::to_vec_pretty(value).map_err(|e| format!("encode JSON: {e}"))?;
    fs::write(&temporary, encoded).map_err(|e| format!("write {}: {e}", temporary.display()))?;
    fs::rename(&temporary, path).map_err(|e| format!("replace {}: {e}", path.display()))
}

#[cfg(test)]
mod tests {
    use super::*;

    const STATE_FILES: &[&str] = &["desktop-settings.json"];

    #[test]
    fn manifest_paths_cannot_escape_backup() {
        assert!(safe_relative_path("coordinator.tar.gz").is_ok());
        assert!(safe_relative_path("app-state/ui-state.json").is_ok());
        assert!(safe_relative_path("../ui-state.json").is_err());
        assert!(safe_relative_path("/tmp/ui-state.json").is_err());
    }

    #[test]
    fn deployment_volumes_are_named_and_checked() {
        let volume = "commitarium-preview-prj_todo-live.api-data";
        assert_eq!(
            deployment_volume(&format!("{DEPLOYMENT_PREFIX}{volume}")).as_deref(),
            Some(volume)
        );
        assert_eq!(volume_project(volume), "commitarium-preview-prj_todo-live");
        // authentik's database is backed up the same way.
        let database = "commitarium-authentik.database";
        assert_eq!(
            deployment_volume(&format!("{DEPLOYMENT_PREFIX}{database}")).as_deref(),
            Some(database)
        );
        assert_eq!(volume_project(database), crate::authentik::PROJECT);
        // Not a deployment's: a preview's, another project's, or unsafe.
        for name in [
            "commitarium-preview-prj_todo.api-data",
            "commitarium_coordinator-data",
            "commitarium-preview-prj_todo-live.x;rm -rf",
        ] {
            assert!(
                deployment_volume(&format!("{DEPLOYMENT_PREFIX}{name}")).is_none(),
                "{name}"
            );
        }
    }

    #[test]
    fn a_restore_reads_the_archive_before_it_empties_the_volume() {
        let args = restore_args("image", "container", &CORE_COMPONENTS[0]);
        assert!(args.iter().any(|arg| arg == "-i"));
        let script = args.last().expect("script");
        let unpack = script.find("/bin/tar").expect("unpack");
        let empty = script.find("rm -rf -- {}").expect("empty");
        assert!(unpack < empty, "{script}");
    }

    #[test]
    fn backup_manifest_requires_and_verifies_every_core_archive() {
        let root = tempfile::tempdir().expect("backup root");
        let mut components = Vec::new();
        let mut hashes = BTreeMap::new();
        for component in CORE_COMPONENTS.iter().filter(|item| item.required) {
            let name = format!("{}.tar.gz", component.name);
            fs::write(root.path().join(&name), component.name.as_bytes()).expect("archive fixture");
            hashes.insert(
                name,
                sha256_file(&root.path().join(format!("{}.tar.gz", component.name)))
                    .expect("checksum"),
            );
            components.push(component.name.to_string());
        }
        write_json_atomic(
            &root.path().join("manifest.json"),
            &BackupManifest {
                format: BACKUP_FORMAT.to_string(),
                format_version: BACKUP_FORMAT_VERSION,
                app_version: "test".to_string(),
                created_at_unix_seconds: 1,
                credentials_included: false,
                components,
                sha256: hashes,
            },
        )
        .expect("manifest");
        read_backup_manifest(root.path(), STATE_FILES).expect("valid backup");

        fs::write(root.path().join("coordinator.tar.gz"), "corrupt").expect("corrupt archive");
        let error = read_backup_manifest(root.path(), STATE_FILES).expect_err("checksum must fail");
        assert!(error.contains("checksum failed"));
    }
}
