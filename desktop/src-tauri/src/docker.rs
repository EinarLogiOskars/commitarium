//! Trusted host-side Docker / Compose launcher.
//!
//! These are the only privileged host operations the renderer can reach, and
//! they are deliberately narrow: every Compose action is scoped to
//! Commitarium's own project name and Compose file. This is not a general
//! Compose or shell runner — the renderer cannot pass an arbitrary file,
//! project, or command through to Docker.

use serde::Serialize;
use std::ffi::OsStr;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use tauri::{AppHandle, Manager, State};

use crate::{agents, bootstrap, cleanup, cli_updates, environment, profiles};
use commitarium_host::docker::{
    compose_command as host_compose_command, compose_files as host_compose_files,
    docker_desktop_path, docker_executable, COMPOSE_OVERRIDE_ENV, ENVIRONMENT_COMPOSE_ENV,
};
pub(crate) use commitarium_host::docker::{
    compose_file, docker_command, PROJECT_NAME, PROVIDER_PROFILES,
};

/// Official Docker installation page, surfaced to the user when Docker is
/// missing. A fixed, trusted URL — never sourced from runtime data.
const DOCKER_INSTALL_URL: &str = "https://docs.docker.com/get-docker/";

const ENVIRONMENT_WARNING: &str = "Your agents may still be on the previous version, because Commitarium couldn't update their tools. It will keep trying in the background. Until then, work may fail to start.";
const TEMPLATE_PROFILE: &str = "agent-template";
const TEMPLATE_SERVICES: &[&str] = &["codex-agent-template", "claude-agent-template"];

/// Worker services from before agents (ADR-016); their containers are retired.
const RETIRED_WORKER_SERVICES: &[&str] = &[
    "codex-worker",
    "codex-reviewer-worker",
    "claude-worker",
    "claude-reviewer-worker",
];

/// Services that do not contain provider credentials and are useful even when
/// no real agent profile has been connected yet.
const CORE_SERVICES: &[&str] = &["coordinator", "simulated-codex-worker"];

const RELEASE_MODE_ENV: &str = "COMMITARIUM_RELEASE_MODE";
const EMBEDDED_COMPOSE: &str = include_str!("../../../compose.yml");
const EMBEDDED_RELEASE_COMPOSE: &str = include_str!("../../../compose.release.yml");
/// Referenced by the Codex agent worker template relative to the Compose file.
const EMBEDDED_CODEX_SECCOMP: &str = include_str!("../../../docker/codex-seccomp.json");
const CODEX_SECCOMP_PATH: &str = "docker/codex-seccomp.json";

/// Result of probing the host for Docker and Compose readiness.
#[derive(Serialize)]
pub struct DockerProbe {
    docker_installed: bool,
    docker_running: bool,
    compose_available: bool,
    docker_launchable: bool,
    docker_version: Option<String>,
    compose_version: Option<String>,
    install_url: &'static str,
}

/// Install the release Compose definitions into the per-user data directory.
/// Development builds keep resolving the repository Compose file, and an
/// explicit environment override always wins for tests and advanced use.
pub(crate) fn prepare_runtime(app: &AppHandle) -> Result<(), String> {
    let runtime_dir = app
        .path()
        .app_data_dir()
        .map_err(|e| format!("resolve app data directory: {e}"))?
        .join("runtime");
    fs::create_dir_all(&runtime_dir)
        .map_err(|e| format!("create Docker runtime directory: {e}"))?;
    if std::env::var_os(ENVIRONMENT_COMPOSE_ENV).is_none() {
        std::env::set_var(
            ENVIRONMENT_COMPOSE_ENV,
            runtime_dir.join("compose.environment.yml"),
        );
    }
    agents::init_overlay_path(&runtime_dir);
    // Best effort: a failed rewrite leaves the old overlay, whose role
    // services are then only created to be retired.
    let _ = environment::migrate_environment_overlay();
    let compose_override = std::env::var_os("COMMITARIUM_COMPOSE_FILE");
    if is_explicit_override(compose_override.as_deref(), &runtime_dir) || cfg!(debug_assertions) {
        return Ok(());
    }

    let (compose_file, release_file) = materialize_release_compose(&runtime_dir)?;

    std::env::set_var("COMMITARIUM_COMPOSE_FILE", compose_file);
    std::env::set_var(COMPOSE_OVERRIDE_ENV, release_file);
    std::env::set_var("COMMITARIUM_IMAGE_TAG", env!("CARGO_PKG_VERSION"));
    std::env::set_var(RELEASE_MODE_ENV, "1");
    Ok(())
}

/// A restart after an update inherits this process's environment, so the
/// runtime's own Compose file there is the previous version's setting, which
/// must be replaced along with its image tag, not an override to respect.
fn is_explicit_override(compose_file: Option<&OsStr>, runtime_dir: &Path) -> bool {
    compose_file.is_some_and(|file| Path::new(file) != runtime_dir.join("compose.yml"))
}

fn materialize_release_compose(runtime_dir: &Path) -> Result<(PathBuf, PathBuf), String> {
    fs::create_dir_all(runtime_dir).map_err(|e| format!("create Docker runtime directory: {e}"))?;
    let compose_file = runtime_dir.join("compose.yml");
    let release_file = runtime_dir.join("compose.release.yml");
    fs::write(&compose_file, EMBEDDED_COMPOSE)
        .map_err(|e| format!("write embedded Compose definition: {e}"))?;
    fs::write(&release_file, EMBEDDED_RELEASE_COMPOSE)
        .map_err(|e| format!("write embedded release Compose definition: {e}"))?;
    let seccomp_file = runtime_dir.join(CODEX_SECCOMP_PATH);
    if let Some(parent) = seccomp_file.parent() {
        fs::create_dir_all(parent).map_err(|e| format!("create seccomp profile directory: {e}"))?;
    }
    fs::write(&seccomp_file, EMBEDDED_CODEX_SECCOMP)
        .map_err(|e| format!("write embedded Codex seccomp profile: {e}"))?;
    Ok((compose_file, release_file))
}

pub(crate) fn environment_compose_file() -> Result<PathBuf, String> {
    std::env::var_os(ENVIRONMENT_COMPOSE_ENV)
        .map(PathBuf::from)
        .ok_or_else(|| "environment Compose path was not initialized".to_string())
}

/// The complete Compose file set, with the generated agents overlay.
pub(crate) fn compose_files() -> Result<Vec<PathBuf>, String> {
    host_compose_files(agents::existing_overlay())
}

pub(crate) fn append_compose_files(command: &mut Command) -> Result<(), String> {
    for file in compose_files()? {
        command.arg("-f").arg(file);
    }
    Ok(())
}

pub(crate) fn release_mode() -> bool {
    std::env::var(RELEASE_MODE_ENV).as_deref() == Ok("1")
}

/// Run `docker compose [--profile <fixed profile>...] -f <file> -p
/// commitarium <args…>`
/// scoped to the Commitarium project and its Compose file — not a general
/// Compose runner. Both profile names and service names come only from trusted
/// constants or the fixed provider-profile table, never from the renderer.
pub(crate) fn compose(profiles: &[&str], args: &[&str]) -> Result<String, String> {
    compose_command(profiles, args, true, true)
}

/// Execute against the shipped definitions alone. Both generated overlays can
/// name locally built environment images, which a pull could never fetch.
pub(crate) fn compose_base(profiles: &[&str], args: &[&str]) -> Result<String, String> {
    compose_command(profiles, args, false, false)
}

/// The resolved Compose model with the agent worker templates, including the
/// approved-environment images but not the generated agents overlay itself.
pub(crate) fn template_config(template_profile: &str) -> Result<String, String> {
    compose_command(
        &[template_profile],
        &["config", "--format", "json"],
        true,
        false,
    )
}

fn compose_command(
    profiles: &[&str],
    args: &[&str],
    include_environment: bool,
    include_agents: bool,
) -> Result<String, String> {
    let overlay = include_agents.then(agents::existing_overlay).flatten();
    host_compose_command(profiles, args, include_environment, overlay)
}

/// Return the trusted image reference selected by the base Compose files for a
/// fixed service. Development definitions without an explicit image use
/// Compose's deterministic project/service build tag.
pub(crate) fn configured_service_image(service: &str) -> Result<String, String> {
    if !TEMPLATE_SERVICES.contains(&service) {
        return Err("service is not an agent worker template".to_string());
    }
    let configured = compose_command(
        &[TEMPLATE_PROFILE],
        &["config", "--format", "json"],
        false,
        false,
    )?;
    configured_service_image_from_json(&configured, service)
}

fn configured_service_image_from_json(configured: &str, service: &str) -> Result<String, String> {
    let value: serde_json::Value = serde_json::from_str(configured)
        .map_err(|e| format!("decode base Compose configuration: {e}"))?;
    let image = value
        .get("services")
        .and_then(|services| services.get(service))
        .and_then(|service| service.get("image"))
        .and_then(serde_json::Value::as_str)
        .map(str::to_string)
        .unwrap_or_else(|| format!("{PROJECT_NAME}-{service}"));
    if image.is_empty()
        || !image.bytes().all(|byte| {
            byte.is_ascii_alphanumeric() || matches!(byte, b':' | b'-' | b'_' | b'.' | b'/' | b'@')
        })
    {
        return Err(format!(
            "base Compose selected an unsafe image for {service}"
        ));
    }
    Ok(image)
}

/// Run a plain command and return its trimmed stdout when it exits zero.
fn probe(program: &str, args: &[&str]) -> Option<String> {
    let output = crate::process::command(program).args(args).output().ok()?;
    if output.status.success() {
        Some(String::from_utf8_lossy(&output.stdout).trim().to_string())
    } else {
        None
    }
}

fn probe_docker(args: &[&str]) -> Option<String> {
    let executable = docker_executable()?;
    probe(executable.to_string_lossy().as_ref(), args)
}

fn docker_probe_blocking() -> DockerProbe {
    let executable_found = docker_executable().is_some();
    let docker_version = probe_docker(&["--version"]);
    // `docker info --format {{.ServerVersion}}` only succeeds when the daemon
    // is actually reachable, so it doubles as the "is the daemon running" check.
    let server_version = probe_docker(&["info", "--format", "{{.ServerVersion}}"]);
    let compose_version = probe_docker(&["compose", "version", "--short"]);
    let desktop_path = docker_desktop_path();

    DockerProbe {
        docker_installed: executable_found || desktop_path.is_some(),
        docker_running: server_version.is_some(),
        compose_available: compose_version.is_some(),
        docker_launchable: desktop_path.is_some(),
        docker_version,
        compose_version,
        install_url: DOCKER_INSTALL_URL,
    }
}

/// Probe the host for Docker and Compose readiness without blocking the app's
/// event loop while Docker Desktop is still starting.
#[tauri::command]
pub async fn docker_probe() -> Result<DockerProbe, String> {
    tauri::async_runtime::spawn_blocking(docker_probe_blocking)
        .await
        .map_err(|_| "could not inspect Docker Desktop".to_string())
}

/// Launch only a detected Docker Desktop installation. The renderer cannot
/// supply a program or arguments, preserving the native command boundary.
#[tauri::command]
pub fn launch_docker_desktop() -> Result<(), String> {
    let path = docker_desktop_path()
        .ok_or_else(|| "Docker Desktop is not installed in a supported location".to_string())?;

    #[cfg(target_os = "macos")]
    let mut command = {
        let mut command = crate::process::command("/usr/bin/open");
        command.arg(path);
        command
    };

    #[cfg(any(target_os = "windows", target_os = "linux"))]
    let mut command = crate::process::command(path);

    #[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
    let mut command = crate::process::command(path);

    command
        .spawn()
        .map(|_| ())
        .map_err(|e| format!("could not launch Docker Desktop: {e}"))
}

/// Bring core services up and reconcile every real role worker against its
/// exact provider profile. A disconnected, expired, or failed profile keeps
/// only its matching worker stopped; it does not prevent the app from opening.
/// Returns a warning for the user when the stack is up but something needs
/// their attention.
#[tauri::command]
pub async fn stack_up(
    app: AppHandle,
    manager: State<'_, profiles::ProfileManager>,
) -> Result<Option<String>, String> {
    let manager = manager.inner().clone();
    let warning = tauri::async_runtime::spawn_blocking(move || start_stack(&manager))
        .await
        .map_err(|_| "could not start the Commitarium stack".to_string())??;
    // The workers are up: check their CLIs now rather than at the next retry.
    cli_updates::wake(&app);
    Ok(warning)
}

pub(crate) fn stack_up_with_manager(manager: &profiles::ProfileManager) -> Result<(), String> {
    start_stack(manager).map(|_| ())
}

fn start_stack(manager: &profiles::ProfileManager) -> Result<Option<String>, String> {
    let file = compose_file()?;
    let transport_changed = bootstrap::prepare_transport_secrets(&file)?;
    if release_mode() {
        compose_base(PROVIDER_PROFILES, &["pull", "--policy", "missing"])?;
    }
    // Forgejo must exist before its own admin CLI can create the internal
    // identities and tokens required by the other services.
    start_forgejo()?;
    // Preview leftovers are swept once, as the app starts (lib.rs): this
    // runs again on every window reload and agent sign-in, while previews
    // run, and sweeping here removed their folders and containers.
    let forgejo_changed = bootstrap::provision_forgejo(&file, PROJECT_NAME)?;
    let agents_changed = refresh_agent_identities(&file);
    let credentials_changed = transport_changed || forgejo_changed;
    start_core_services(credentials_changed)?;
    let warning = prepare_environment_images();
    reconcile_agent_workers(manager, credentials_changed || agents_changed)?;
    Ok(warning)
}

/// Tear the whole Commitarium stack down (project-wide, all profiles).
#[tauri::command]
pub async fn stack_down() -> Result<(), String> {
    tauri::async_runtime::spawn_blocking(stack_down_blocking)
        .await
        .map_err(|_| "could not stop the Commitarium stack".to_string())?
}

pub(crate) fn stack_down_blocking() -> Result<(), String> {
    // Workers no longer defined (the pre-agent role workers, removed agents)
    // would otherwise keep the project network in use.
    compose(PROVIDER_PROFILES, &["down", "--remove-orphans"]).map(|_| ())
}

/// Update the stack: pull the latest images, then recreate in the background.
#[tauri::command]
pub async fn stack_update(
    app: AppHandle,
    manager: State<'_, profiles::ProfileManager>,
) -> Result<Option<String>, String> {
    let manager = manager.inner().clone();
    let warning = tauri::async_runtime::spawn_blocking(move || stack_update_with_manager(&manager))
        .await
        .map_err(|_| "could not update the Commitarium stack".to_string())??;
    cli_updates::wake(&app);
    Ok(warning)
}

fn stack_update_with_manager(manager: &profiles::ProfileManager) -> Result<Option<String>, String> {
    let file = compose_file()?;
    let transport_changed = bootstrap::prepare_transport_secrets(&file)?;
    compose_base(PROVIDER_PROFILES, &["pull"])?;
    start_forgejo()?;
    let forgejo_changed = bootstrap::provision_forgejo(&file, PROJECT_NAME)?;
    let agents_changed = refresh_agent_identities(&file);
    let credentials_changed = transport_changed || forgejo_changed;
    start_core_services(credentials_changed)?;
    let warning = prepare_environment_images();
    reconcile_agent_workers(manager, credentials_changed || agents_changed)?;
    Ok(warning)
}

/// Bring the known agents' Forgejo tokens up to date (scopes, the one-time
/// rotation, revoking the tokens they replaced) as soon as Forgejo is ready,
/// so it does not depend on the later startup steps succeeding. The agents
/// come from the coordinator or, while it is still stopped, its last list;
/// reconciling the workers repeats this for any agent missing here. A
/// failure is logged and retried by that step and on the next start.
fn refresh_agent_identities(file: &Path) -> bool {
    let agents = agents::current().unwrap_or_default();
    bootstrap::provision_agent_identities(file, PROJECT_NAME, &agents, true).unwrap_or_else(
        |error| {
            eprintln!("[bootstrap] could not refresh agent Forgejo tokens: {error}");
            false
        },
    )
}

fn start_forgejo() -> Result<(), String> {
    let mut args = vec!["up", "-d"];
    if release_mode() {
        args.push("--no-build");
    }
    args.push("forgejo");
    compose(&[], &args).map(|_| ())
}

fn start_core_services(force_recreate: bool) -> Result<(), String> {
    let mut args = vec!["up", "-d"];
    if release_mode() {
        args.push("--no-build");
    }
    if force_recreate {
        args.push("--force-recreate");
    }
    args.extend_from_slice(CORE_SERVICES);
    compose(&[], &args).map(|_| ())
}

/// Give every agent its worker (ADR-016): read the agents from the running
/// coordinator, provision each one's bearer token and Forgejo identity pair,
/// regenerate the agents overlay, retire workers of removed agents and the
/// fixed pre-agent workers, then run only the workers whose login is
/// connected. A disconnected, expired, or failed login keeps only that
/// agent's worker stopped.
pub(crate) fn reconcile_agent_workers(
    manager: &profiles::ProfileManager,
    force_recreate: bool,
) -> Result<(), String> {
    let agents = agents::fetch(true)?;
    let force_recreate = provision_agents(&agents, true)? || force_recreate;
    let services: Vec<String> = agents
        .iter()
        .map(|agent| agents::worker_service(&agent.id))
        .collect();
    retire_stale_workers(&services)?;

    let connections = profiles::worker_connections(manager, &agents);
    let stopped: Vec<&str> = connections
        .iter()
        .filter(|connection| !connection.connected)
        .map(|connection| connection.service.as_str())
        .collect();
    if !stopped.is_empty() {
        let mut args = vec!["stop"];
        args.extend(stopped.iter().copied());
        compose(PROVIDER_PROFILES, &args)?;
        if force_recreate {
            let mut args = vec!["create", "--force-recreate"];
            if release_mode() {
                args.push("--no-build");
            }
            args.extend(stopped);
            compose(PROVIDER_PROFILES, &args)?;
        }
    }

    let connected: Vec<&str> = connections
        .iter()
        .filter(|connection| connection.connected)
        .map(|connection| connection.service.as_str())
        .collect();
    if !connected.is_empty() {
        // Wait until the workers are healthy, so whoever reconciled them (an
        // approved environment resuming its run, say) can reach them at once.
        let mut args = vec!["up", "-d", "--wait", "--wait-timeout", "120"];
        if release_mode() {
            args.push("--no-build");
        }
        if force_recreate {
            args.push("--force-recreate");
        }
        args.extend(connected);
        compose(PROVIDER_PROFILES, &args)?;
    }
    // The stack is up on its current images; the ones it replaced can go.
    cleanup::spawn();
    Ok(())
}

/// Move an approved environment onto this version's base images before the
/// workers start, so no agent runs on the previous version's image. Without
/// this the workers would wait for the background refresh. Best effort: that
/// refresh retries, and the workers can still start on what is here, so a
/// failure comes back as a warning for the user.
fn prepare_environment_images() -> Option<String> {
    if !release_mode() {
        return None;
    }
    let refreshed = agents::fetch(true).and_then(|agents| {
        pull_agent_base_images(&agents)?;
        environment::refresh_stale_images()
    });
    let error = refreshed.err()?;
    eprintln!("[stack] could not refresh the approved environment: {error}");
    Some(ENVIRONMENT_WARNING.to_string())
}

/// Bring the base worker images of the providers in use. With an approved
/// environment the workers run derivatives, so nothing else fetches them.
fn pull_agent_base_images(agents: &[agents::Agent]) -> Result<(), String> {
    let mut args = vec!["pull", "--policy", "missing"];
    for provider in [agents::Provider::Codex, agents::Provider::Claude] {
        if agents.iter().any(|agent| agent.provider == provider) {
            args.push(provider.template_service());
        }
    }
    if args.len() > 3 {
        compose_base(&[TEMPLATE_PROFILE], &args)?;
    }
    Ok(())
}

/// Make each agent's worker addressable: its bearer token, its Forgejo
/// identity pair, and its service in the agents overlay. With `regenerate`
/// the overlay is rebuilt from the templates (for example after an approved
/// environment changed their images); otherwise only when agents changed.
/// Only `regenerate` (the reconcile path, which recreates the workers) also
/// replaces existing Forgejo tokens that are out of date. Returns whether any
/// credential was created.
pub(crate) fn provision_agents(agents: &[agents::Agent], regenerate: bool) -> Result<bool, String> {
    let file = compose_file()?;
    let tokens_changed = bootstrap::prepare_agent_worker_tokens(&file, agents)?;
    let identities_changed =
        bootstrap::provision_agent_identities(&file, PROJECT_NAME, agents, regenerate)?;
    if regenerate || !agents::overlay_matches(agents) {
        agents::write_overlay(agents)?;
    }
    Ok(tokens_changed || identities_changed)
}

/// Remove the containers of worker services that are no longer defined: the
/// fixed pre-agent workers and the workers of removed agents. Their volumes
/// are kept; only containers are removed.
fn retire_stale_workers(current: &[String]) -> Result<(), String> {
    let output = docker_command()
        .args([
            "ps",
            "--all",
            "--filter",
            &format!("label=com.docker.compose.project={PROJECT_NAME}"),
            "--format",
            "{{.ID}} {{.Label \"com.docker.compose.service\"}}",
        ])
        .output()
        .map_err(|e| format!("list Commitarium containers: {e}"))?;
    if !output.status.success() {
        return Err("could not list Commitarium containers".to_string());
    }
    let stale = stale_worker_containers(&String::from_utf8_lossy(&output.stdout), current);
    if stale.is_empty() {
        return Ok(());
    }
    let status = docker_command()
        .args(["rm", "--force"])
        .args(&stale)
        .output()
        .map_err(|e| format!("remove retired worker containers: {e}"))?;
    if !status.status.success() {
        return Err("could not remove retired worker containers".to_string());
    }
    Ok(())
}

fn stale_worker_containers(listing: &str, current: &[String]) -> Vec<String> {
    listing
        .lines()
        .filter_map(|line| line.split_once(' '))
        .filter(|(id, service)| {
            !id.is_empty()
                && id.bytes().all(|byte| byte.is_ascii_hexdigit())
                && (RETIRED_WORKER_SERVICES.contains(service)
                    || (service.starts_with("agent-")
                        && service.ends_with("-worker")
                        && !current.iter().any(|known| known == service)))
        })
        .map(|(id, _)| id.to_string())
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_restart_after_an_update_replaces_the_old_runtime_settings() {
        let runtime = Path::new("/data/runtime");
        assert!(!is_explicit_override(None, runtime));
        assert!(!is_explicit_override(
            Some(OsStr::new("/data/runtime/compose.yml")),
            runtime
        ));
        assert!(is_explicit_override(
            Some(OsStr::new("/elsewhere/compose.yml")),
            runtime
        ));
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn docker_command_exposes_desktop_credential_helpers() {
        let Some(app) = docker_desktop_path() else {
            return;
        };
        let bundled_bin = app.join("Contents/Resources/bin");
        let command = docker_command();
        let configured_path = command
            .get_envs()
            .find(|(name, _)| *name == std::ffi::OsStr::new("PATH"))
            .and_then(|(_, value)| value)
            .expect("Docker command should carry an explicit PATH");

        assert!(std::env::split_paths(configured_path).any(|path| path == bundled_bin));
        assert!(bundled_bin.join("docker-credential-osxkeychain").is_file());
    }

    #[test]
    fn materializes_embedded_release_compose_files() {
        let root = tempfile::tempdir().unwrap();
        let (base, release) = materialize_release_compose(root.path()).unwrap();

        assert_eq!(fs::read_to_string(base).unwrap(), EMBEDDED_COMPOSE);
        assert_eq!(
            fs::read_to_string(release).unwrap(),
            EMBEDDED_RELEASE_COMPOSE
        );
        assert!(EMBEDDED_RELEASE_COMPOSE.contains("build: !reset null"));
        assert!(EMBEDDED_RELEASE_COMPOSE.contains("ghcr.io/einarlogioskars"));
        assert!(EMBEDDED_RELEASE_COMPOSE.contains("COMMITARIUM_RUNNER_MODE: real_agents"));
        assert_eq!(
            fs::read_to_string(root.path().join(CODEX_SECCOMP_PATH)).unwrap(),
            EMBEDDED_CODEX_SECCOMP
        );
        assert!(!EMBEDDED_COMPOSE.contains("seccomp=unconfined"));
        assert_eq!(
            EMBEDDED_COMPOSE
                .matches(&format!("seccomp={CODEX_SECCOMP_PATH}"))
                .count(),
            1,
            "the Codex agent worker template must allow the nested read-only bwrap sandbox"
        );
        assert_eq!(
            EMBEDDED_COMPOSE.matches("no-new-privileges:true").count(),
            2
        );
    }

    #[test]
    fn resolves_only_safe_base_service_images() {
        let configured = r#"{"services":{"codex-agent-template":{"image":"ghcr.io/example/codex:1.2.3"},"claude-agent-template":{"build":{"context":"."}}}}"#;
        assert_eq!(
            configured_service_image_from_json(configured, "codex-agent-template").unwrap(),
            "ghcr.io/example/codex:1.2.3"
        );
        assert_eq!(
            configured_service_image_from_json(configured, "claude-agent-template").unwrap(),
            "commitarium-claude-agent-template"
        );
        let unsafe_configured =
            r#"{"services":{"codex-agent-template":{"image":"safe;touch-host"}}}"#;
        assert!(
            configured_service_image_from_json(unsafe_configured, "codex-agent-template").is_err()
        );
    }

    #[test]
    fn retires_pre_agent_workers_and_workers_of_removed_agents() {
        let listing = "aaa111 coordinator\nbbb222 codex-reviewer-worker\nccc333 agent-codex-worker\nddd444 agent-gone-worker\nzzz agent-gone-worker\n";
        let current = vec!["agent-codex-worker".to_string()];
        assert_eq!(
            stale_worker_containers(listing, &current),
            ["bbb222", "ddd444"]
        );
    }

    /// Exercises the same internal entry points as the app against a
    /// real development stack. It is opt-in because it starts/reconciles
    /// containers and requires Docker plus the provider images.
    #[test]
    #[ignore = "requires and mutates the local Commitarium Docker stack"]
    fn live_start_matches_each_provider_connection() {
        let manager = profiles::ProfileManager::new();

        stack_up_with_manager(&manager).unwrap();
        let expected = profiles::worker_connections(&manager, &agents::fetch(false).unwrap());
        let statuses = commitarium_host::docker::stack_status().unwrap();

        for core in ["forgejo", "coordinator", "simulated-codex-worker"] {
            assert!(statuses
                .iter()
                .any(|status| status.service == core && status.state == "running"));
        }
        for connection in expected {
            let running = statuses
                .iter()
                .any(|status| status.service == connection.service && status.state == "running");
            assert_eq!(running, connection.connected, "{}", connection.service);
        }
    }
}
