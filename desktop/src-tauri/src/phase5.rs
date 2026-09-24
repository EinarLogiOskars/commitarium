//! Phase 5 trusted-desktop services.
//!
//! The renderer can manage typed presentation preferences, submit a bounded
//! notification, inspect local disk readiness, and select a backup directory.
//! It cannot choose Docker images, containers, volume paths, archive members,
//! or commands.

use fs2::available_space;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::fs::{self, File};
use std::io::Read;
use std::path::{Component, Path, PathBuf};
use std::process::Stdio;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tauri::{AppHandle, Manager, State};
use tauri_plugin_notification::NotificationExt;

use crate::{bootstrap, docker, profiles};

const SETTINGS_FILE: &str = "desktop-settings.json";
const NOTIFICATION_LEDGER_FILE: &str = "notification-ledger.json";
const BACKUP_FORMAT: &str = "commitarium-backup";
const BACKUP_FORMAT_VERSION: u32 = 1;
const MAX_NOTIFICATION_IDS: usize = 2_000;
pub const EXIT_CONFIRMATION_EVENT: &str = "exit-confirmation-requested";
pub const EXIT_CONFIRMATION_TIMEOUT: Duration = Duration::from_secs(30);

const APP_STATE_FILES: &[&str] = &[
    SETTINGS_FILE,
    NOTIFICATION_LEDGER_FILE,
    "ui-state.json",
    "project-sources.json",
    "project-remote-setup-receipts.json",
    "handoff-receipts.json",
    "folder-handoff-receipts.json",
    "project-handoff-receipts.json",
    "project-upstream-receipts.json",
    "project-workspace-setup-receipts.json",
    "upstream-publication-receipts.json",
];

const BACKUP_COMPONENTS: &[BackupComponent] = &[
    BackupComponent {
        name: "coordinator",
        service: "coordinator",
        source: "/var/lib/commitarium",
        owner: "65532:65532",
        required: true,
    },
    BackupComponent {
        name: "forgejo",
        service: "forgejo",
        source: "/var/lib/gitea",
        owner: "1000:1000",
        required: true,
    },
    BackupComponent {
        name: "workspaces",
        service: "coordinator",
        source: "/workspaces",
        owner: "65532:65532",
        required: true,
    },
    BackupComponent {
        name: "toolchains",
        service: "coordinator",
        source: "/var/lib/commitarium-toolchains",
        owner: "65532:65532",
        required: true,
    },
    BackupComponent {
        name: "simulated-worker-journal",
        service: "simulated-codex-worker",
        source: "/var/lib/commitarium-worker",
        owner: "65532:65532",
        required: false,
    },
    BackupComponent {
        name: "codex-worker-journal",
        service: "codex-worker",
        source: "/var/lib/commitarium-worker",
        owner: "65532:65532",
        required: false,
    },
    BackupComponent {
        name: "codex-reviewer-worker-journal",
        service: "codex-reviewer-worker",
        source: "/var/lib/commitarium-worker",
        owner: "65532:65532",
        required: false,
    },
    BackupComponent {
        name: "claude-worker-journal",
        service: "claude-worker",
        source: "/var/lib/commitarium-worker",
        owner: "65532:65532",
        required: false,
    },
    BackupComponent {
        name: "claude-reviewer-worker-journal",
        service: "claude-reviewer-worker",
        source: "/var/lib/commitarium-worker",
        owner: "65532:65532",
        required: false,
    },
];

#[derive(Clone, Copy)]
struct BackupComponent {
    name: &'static str,
    service: &'static str,
    source: &'static str,
    owner: &'static str,
    required: bool,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ExitBehavior {
    KeepRunning,
    StopStack,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(default, deny_unknown_fields)]
pub struct DesktopSettings {
    pub notifications_configured: bool,
    pub notifications_enabled: bool,
    pub notify_attention: bool,
    pub notify_failures: bool,
    pub notify_auto_merges: bool,
    pub exit_behavior: ExitBehavior,
}

impl Default for DesktopSettings {
    fn default() -> Self {
        Self {
            notifications_configured: false,
            notifications_enabled: false,
            notify_attention: true,
            notify_failures: true,
            notify_auto_merges: true,
            exit_behavior: ExitBehavior::KeepRunning,
        }
    }
}

#[derive(Clone, Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct NativeNotification {
    event_id: String,
    kind: NotificationKind,
    title: String,
    body: String,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "snake_case")]
enum NotificationKind {
    Attention,
    Failure,
    AutoMerge,
}

#[derive(Clone, Debug, Serialize)]
pub struct NotificationDelivery {
    delivered: bool,
    reason: &'static str,
}

#[derive(Clone, Debug, Serialize)]
pub struct DiskSpaceProbe {
    path: String,
    available_bytes: u64,
    recommended_free_bytes: u64,
    backup_ready: bool,
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

pub struct ExitCoordinator {
    state: Mutex<ExitCoordinatorState>,
}

#[derive(Debug)]
struct ExitCoordinatorState {
    phase: ExitPhase,
    next_request_id: u64,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum ExitPhase {
    Idle,
    Pending(u64),
    ShuttingDown,
    ReadyToExit,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum ExitRequestAction {
    Prompt(u64),
    Wait,
    Allow,
}

#[derive(Clone, Copy, Debug, Serialize)]
pub struct ExitConfirmationRequested {
    timeout_ms: u64,
}

impl ExitConfirmationRequested {
    pub fn new() -> Self {
        Self {
            timeout_ms: EXIT_CONFIRMATION_TIMEOUT.as_millis() as u64,
        }
    }
}

impl Default for ExitConfirmationRequested {
    fn default() -> Self {
        Self::new()
    }
}

impl Default for ExitCoordinator {
    fn default() -> Self {
        Self {
            state: Mutex::new(ExitCoordinatorState {
                phase: ExitPhase::Idle,
                next_request_id: 1,
            }),
        }
    }
}

impl ExitCoordinator {
    fn begin(&self) -> ExitRequestAction {
        let mut state = self.state.lock().unwrap_or_else(|error| error.into_inner());
        match state.phase {
            ExitPhase::Idle => {
                let request_id = state.next_request_id;
                state.next_request_id = state.next_request_id.wrapping_add(1);
                state.phase = ExitPhase::Pending(request_id);
                ExitRequestAction::Prompt(request_id)
            }
            ExitPhase::Pending(_) | ExitPhase::ShuttingDown => ExitRequestAction::Wait,
            ExitPhase::ReadyToExit => ExitRequestAction::Allow,
        }
    }

    fn confirm(&self) -> bool {
        let mut state = self.state.lock().unwrap_or_else(|error| error.into_inner());
        if matches!(state.phase, ExitPhase::Pending(_)) {
            state.phase = ExitPhase::ShuttingDown;
            true
        } else {
            false
        }
    }

    fn cancel(&self) -> bool {
        let mut state = self.state.lock().unwrap_or_else(|error| error.into_inner());
        if matches!(state.phase, ExitPhase::Pending(_)) {
            state.phase = ExitPhase::Idle;
            true
        } else {
            false
        }
    }

    fn timeout(&self, request_id: u64) -> bool {
        let mut state = self.state.lock().unwrap_or_else(|error| error.into_inner());
        if state.phase == ExitPhase::Pending(request_id) {
            state.phase = ExitPhase::ShuttingDown;
            true
        } else {
            false
        }
    }

    fn finish(&self) {
        let mut state = self.state.lock().unwrap_or_else(|error| error.into_inner());
        if state.phase == ExitPhase::ShuttingDown {
            state.phase = ExitPhase::ReadyToExit;
        }
    }
}

#[derive(Default)]
pub struct NotificationCoordinator {
    ledger: Mutex<()>,
}

#[derive(Clone, Default)]
pub struct BackupCoordinator {
    operation: Arc<Mutex<()>>,
}

fn app_data_dir(app: &AppHandle) -> Result<PathBuf, String> {
    let path = app
        .path()
        .app_data_dir()
        .map_err(|e| format!("resolve app data directory: {e}"))?;
    fs::create_dir_all(&path).map_err(|e| format!("create app data directory: {e}"))?;
    Ok(path)
}

fn read_json_or_default<T>(path: &Path) -> Result<T, String>
where
    T: for<'de> Deserialize<'de> + Default,
{
    match fs::read_to_string(path) {
        Ok(contents) => {
            serde_json::from_str(&contents).map_err(|e| format!("parse {}: {e}", path.display()))
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(T::default()),
        Err(error) => Err(format!("read {}: {error}", path.display())),
    }
}

fn write_json_atomic<T: Serialize>(path: &Path, value: &T) -> Result<(), String> {
    let parent = path
        .parent()
        .ok_or_else(|| "settings path has no parent".to_string())?;
    fs::create_dir_all(parent).map_err(|e| format!("create {}: {e}", parent.display()))?;
    let temporary = path.with_extension("json.tmp");
    let encoded = serde_json::to_vec_pretty(value).map_err(|e| format!("encode JSON: {e}"))?;
    fs::write(&temporary, encoded).map_err(|e| format!("write {}: {e}", temporary.display()))?;
    fs::rename(&temporary, path).map_err(|e| format!("replace {}: {e}", path.display()))
}

#[tauri::command]
pub fn load_desktop_settings(app: AppHandle) -> Result<DesktopSettings, String> {
    read_json_or_default(&app_data_dir(&app)?.join(SETTINGS_FILE))
}

#[tauri::command]
pub fn save_desktop_settings(
    app: AppHandle,
    settings: DesktopSettings,
) -> Result<DesktopSettings, String> {
    write_json_atomic(&app_data_dir(&app)?.join(SETTINGS_FILE), &settings)?;
    Ok(settings)
}

fn valid_notification_text(value: &str, maximum: usize) -> bool {
    !value.trim().is_empty() && value.len() <= maximum && !value.contains('\0')
}

#[tauri::command]
pub fn notify_attention(
    app: AppHandle,
    coordinator: State<'_, NotificationCoordinator>,
    notification: NativeNotification,
) -> Result<NotificationDelivery, String> {
    if !valid_notification_text(&notification.event_id, 256)
        || !valid_notification_text(&notification.title, 120)
        || !valid_notification_text(&notification.body, 500)
    {
        return Err("notification fields are invalid".to_string());
    }
    let data = app_data_dir(&app)?;
    let settings: DesktopSettings = read_json_or_default(&data.join(SETTINGS_FILE))?;
    let enabled = settings.notifications_enabled
        && match notification.kind {
            NotificationKind::Attention => settings.notify_attention,
            NotificationKind::Failure => settings.notify_failures,
            NotificationKind::AutoMerge => settings.notify_auto_merges,
        };
    if !enabled {
        return Ok(NotificationDelivery {
            delivered: false,
            reason: "disabled",
        });
    }

    let _guard = coordinator
        .ledger
        .lock()
        .map_err(|_| "notification ledger is unavailable".to_string())?;
    let ledger_path = data.join(NOTIFICATION_LEDGER_FILE);
    let mut ledger: Vec<String> = read_json_or_default(&ledger_path)?;
    if ledger.iter().any(|id| id == &notification.event_id) {
        return Ok(NotificationDelivery {
            delivered: false,
            reason: "duplicate",
        });
    }
    app.notification()
        .builder()
        .title(notification.title)
        .body(notification.body)
        .show()
        .map_err(|e| format!("show desktop notification: {e}"))?;
    ledger.push(notification.event_id);
    if ledger.len() > MAX_NOTIFICATION_IDS {
        ledger.drain(0..ledger.len() - MAX_NOTIFICATION_IDS);
    }
    write_json_atomic(&ledger_path, &ledger)?;
    Ok(NotificationDelivery {
        delivered: true,
        reason: "delivered",
    })
}

#[tauri::command]
pub fn disk_space_probe(app: AppHandle) -> Result<DiskSpaceProbe, String> {
    let path = app_data_dir(&app)?;
    let available = available_space(&path).map_err(|e| format!("inspect free disk space: {e}"))?;
    let recommended = 5 * 1024 * 1024 * 1024;
    Ok(DiskSpaceProbe {
        path: path.to_string_lossy().into_owned(),
        available_bytes: available,
        recommended_free_bytes: recommended,
        backup_ready: available >= recommended,
    })
}

#[tauri::command]
pub async fn create_backup(
    app: AppHandle,
    manager: State<'_, profiles::ProfileManager>,
    backup: State<'_, BackupCoordinator>,
    destination: String,
) -> Result<BackupResult, String> {
    let app = app.clone();
    let manager = manager.inner().clone();
    let backup = backup.inner().clone();
    tauri::async_runtime::spawn_blocking(move || {
        let _guard = backup
            .operation
            .lock()
            .map_err(|_| "backup coordinator is unavailable".to_string())?;
        create_backup_blocking(&app, &manager, Path::new(&destination))
    })
    .await
    .map_err(|_| "backup worker stopped unexpectedly".to_string())?
}

#[tauri::command]
pub async fn restore_backup(
    app: AppHandle,
    manager: State<'_, profiles::ProfileManager>,
    backup: State<'_, BackupCoordinator>,
    source: String,
) -> Result<BackupResult, String> {
    let app = app.clone();
    let manager = manager.inner().clone();
    let backup = backup.inner().clone();
    tauri::async_runtime::spawn_blocking(move || {
        let _guard = backup
            .operation
            .lock()
            .map_err(|_| "backup coordinator is unavailable".to_string())?;
        restore_backup_blocking(&app, &manager, Path::new(&source))
    })
    .await
    .map_err(|_| "restore worker stopped unexpectedly".to_string())?
}

#[tauri::command]
pub async fn inspect_backup(source: String) -> Result<BackupInspection, String> {
    tauri::async_runtime::spawn_blocking(move || {
        let source = Path::new(&source);
        let manifest = read_backup_manifest(source)?;
        Ok(BackupInspection {
            path: source.to_string_lossy().into_owned(),
            format_version: manifest.format_version,
            app_version: manifest.app_version,
            created_at_unix_seconds: manifest.created_at_unix_seconds,
            components: manifest.components,
            credentials_included: manifest.credentials_included,
        })
    })
    .await
    .map_err(|_| "backup inspection worker stopped unexpectedly".to_string())?
}

fn create_backup_blocking(
    app: &AppHandle,
    manager: &profiles::ProfileManager,
    destination: &Path,
) -> Result<BackupResult, String> {
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
    docker::compose(docker::PROVIDER_PROFILES, &["stop"])?;
    let result = export_backup(app, staging.path());
    let restart = if was_running {
        docker::stack_up_with_manager(manager)
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

fn export_backup(app: &AppHandle, destination: &Path) -> Result<BackupManifest, String> {
    let coordinator_id = container_id("coordinator")?
        .ok_or_else(|| "the Commitarium stack has not been created yet".to_string())?;
    let helper_image = container_image(&coordinator_id)?;
    let mut components = Vec::new();
    let mut hashes = BTreeMap::new();

    for component in BACKUP_COMPONENTS {
        let Some(container_id) = container_id(component.service)? else {
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
            &destination.join(&filename),
        )?;
        hashes.insert(
            filename,
            sha256_file(&destination.join(format!("{}.tar.gz", component.name)))?,
        );
        components.push(component.name.to_string());
    }

    let state_dir = destination.join("app-state");
    fs::create_dir_all(&state_dir).map_err(|e| format!("create app-state backup: {e}"))?;
    let app_data = app_data_dir(app)?;
    for name in APP_STATE_FILES {
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

fn restore_backup_blocking(
    app: &AppHandle,
    manager: &profiles::ProfileManager,
    source: &Path,
) -> Result<BackupResult, String> {
    let manifest = read_backup_manifest(source)?;
    let compose_file = docker::compose_file()?;
    bootstrap::prepare_transport_secrets(&compose_file)?;
    bootstrap::prepare_forgejo_secret_mounts(&compose_file)?;
    if docker::release_mode() {
        docker::compose_base(docker::PROVIDER_PROFILES, &["pull", "--policy", "missing"])?;
    }
    let mut create_args = vec!["create"];
    if docker::release_mode() {
        create_args.push("--no-build");
    }
    docker::compose(docker::PROVIDER_PROFILES, &create_args)?;
    docker::compose(docker::PROVIDER_PROFILES, &["stop"])?;

    let coordinator_id = container_id("coordinator")?
        .ok_or_else(|| "could not create the coordinator restore container".to_string())?;
    let helper_image = container_image(&coordinator_id)?;
    let recovery = tempfile::Builder::new()
        .prefix("restore-rollback-")
        .tempdir_in(app_data_dir(app)?)
        .map_err(|e| format!("create restore rollback directory: {e}"))?;
    let recovery_manifest = export_backup(app, recovery.path())?;

    if let Err(restore_error) = apply_backup(app, source, &manifest, &helper_image) {
        let rollback = apply_backup(app, recovery.path(), &recovery_manifest, &helper_image);
        let restart = docker::stack_up_with_manager(manager);
        return match (rollback, restart) {
            (Ok(()), Ok(())) => Err(format!(
                "restore failed and the previous installation was restored: {restore_error}"
            )),
            (rollback, restart) => Err(format!(
                "restore failed ({restore_error}); automatic rollback was incomplete (rollback: {}; restart: {})",
                rollback.err().unwrap_or_else(|| "ok".to_string()),
                restart.err().unwrap_or_else(|| "ok".to_string())
            )),
        };
    }
    docker::stack_up_with_manager(manager)?;
    Ok(BackupResult {
        path: source.to_string_lossy().into_owned(),
        format_version: manifest.format_version,
        components: manifest.components,
        credentials_included: manifest.credentials_included,
    })
}

fn apply_backup(
    app: &AppHandle,
    source: &Path,
    manifest: &BackupManifest,
    helper_image: &str,
) -> Result<(), String> {
    for name in &manifest.components {
        let component = BACKUP_COMPONENTS
            .iter()
            .find(|candidate| candidate.name == name)
            .ok_or_else(|| format!("backup contains unknown component {name}"))?;
        let container_id = container_id(component.service)?.ok_or_else(|| {
            format!(
                "could not create restore container for {}",
                component.service
            )
        })?;
        restore_component(
            helper_image,
            &container_id,
            component,
            &source.join(format!("{}.tar.gz", component.name)),
        )?;
    }
    restore_app_state(app, source)?;
    Ok(())
}

fn read_backup_manifest(source: &Path) -> Result<BackupManifest, String> {
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
        if !BACKUP_COMPONENTS
            .iter()
            .any(|candidate| candidate.name == name)
        {
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
    for required in BACKUP_COMPONENTS
        .iter()
        .filter(|component| component.required)
    {
        if !seen.contains(required.name) {
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
    for name in APP_STATE_FILES {
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

fn restore_app_state(app: &AppHandle, source: &Path) -> Result<(), String> {
    let app_data = app_data_dir(app)?;
    for name in APP_STATE_FILES {
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
    destination: &Path,
) -> Result<(), String> {
    let output =
        File::create(destination).map_err(|e| format!("create {}: {e}", destination.display()))?;
    let status = docker::docker_command()
        .args(["run", "--rm", "--user", "0", "--volumes-from", container_id])
        .args([
            "--entrypoint",
            "/bin/tar",
            image,
            "-C",
            source,
            "-czf",
            "-",
            ".",
        ])
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
    let script = format!(
        "find '{}' -mindepth 1 -maxdepth 1 -exec rm -rf -- {{}} + && /bin/tar -C '{}' -xzf - && chown -R '{}' '{}'",
        component.source, component.source, component.owner, component.source
    );
    let input = File::open(archive).map_err(|e| format!("open {}: {e}", archive.display()))?;
    let output = docker::docker_command()
        .args(["run", "--rm", "--user", "0", "--volumes-from", container_id])
        .args(["--entrypoint", "/bin/sh", image, "-c", &script])
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

fn container_id(service: &str) -> Result<Option<String>, String> {
    let id = docker::compose(docker::PROVIDER_PROFILES, &["ps", "--all", "-q", service])?;
    let id = id.trim().to_string();
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

pub fn exit_behavior(app: &AppHandle) -> ExitBehavior {
    app_data_dir(app)
        .and_then(|data| read_json_or_default::<DesktopSettings>(&data.join(SETTINGS_FILE)))
        .map(|settings| settings.exit_behavior)
        .unwrap_or(ExitBehavior::KeepRunning)
}

pub fn begin_exit_stop(coordinator: &ExitCoordinator) -> ExitRequestAction {
    coordinator.begin()
}

#[tauri::command]
pub fn confirm_exit(
    app: AppHandle,
    coordinator: State<'_, ExitCoordinator>,
    profiles: State<'_, profiles::ProfileManager>,
) -> bool {
    if !coordinator.confirm() {
        return false;
    }
    launch_exit_shutdown(app, profiles.inner().clone());
    true
}

#[tauri::command]
pub fn cancel_exit(coordinator: State<'_, ExitCoordinator>) -> bool {
    coordinator.cancel()
}

pub fn schedule_exit_timeout(app: AppHandle, profiles: profiles::ProfileManager, request_id: u64) {
    std::thread::spawn(move || {
        std::thread::sleep(EXIT_CONFIRMATION_TIMEOUT);
        if app.state::<ExitCoordinator>().inner().timeout(request_id) {
            launch_exit_shutdown(app, profiles);
        }
    });
}

fn launch_exit_shutdown(app: AppHandle, profiles: profiles::ProfileManager) {
    profiles.shutdown();
    std::thread::spawn(move || {
        let _ = docker::stack_down_blocking();
        app.state::<ExitCoordinator>().inner().finish();
        app.exit(0);
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn settings_defaults_require_notification_onboarding_and_keep_stack_running() {
        let settings = DesktopSettings::default();
        assert!(!settings.notifications_configured);
        assert!(!settings.notifications_enabled);
        assert_eq!(settings.exit_behavior, ExitBehavior::KeepRunning);
    }

    #[test]
    fn manifest_paths_cannot_escape_backup() {
        assert!(safe_relative_path("coordinator.tar.gz").is_ok());
        assert!(safe_relative_path("app-state/ui-state.json").is_ok());
        assert!(safe_relative_path("../ui-state.json").is_err());
        assert!(safe_relative_path("/tmp/ui-state.json").is_err());
    }

    #[test]
    fn notification_text_is_bounded() {
        assert!(valid_notification_text("merge:fea_one", 256));
        assert!(!valid_notification_text("", 256));
        assert!(!valid_notification_text("bad\0value", 256));
    }

    #[test]
    fn exit_confirmation_confirm_proceeds_once() {
        let coordinator = ExitCoordinator::default();
        let request_id = match begin_exit_stop(&coordinator) {
            ExitRequestAction::Prompt(request_id) => request_id,
            action => panic!("expected prompt, got {action:?}"),
        };
        assert!(request_id > 0);
        assert_eq!(begin_exit_stop(&coordinator), ExitRequestAction::Wait);
        assert!(coordinator.confirm());
        assert!(!coordinator.confirm());
        assert_eq!(begin_exit_stop(&coordinator), ExitRequestAction::Wait);
        coordinator.finish();
        assert_eq!(begin_exit_stop(&coordinator), ExitRequestAction::Allow);
    }

    #[test]
    fn exit_confirmation_cancel_resets_for_a_later_request() {
        let coordinator = ExitCoordinator::default();
        let first = match begin_exit_stop(&coordinator) {
            ExitRequestAction::Prompt(request_id) => request_id,
            action => panic!("expected prompt, got {action:?}"),
        };
        assert!(coordinator.cancel());
        assert!(!coordinator.cancel());
        let second = match begin_exit_stop(&coordinator) {
            ExitRequestAction::Prompt(request_id) => request_id,
            action => panic!("expected prompt, got {action:?}"),
        };
        assert_ne!(first, second);
        assert!(!coordinator.timeout(first));
    }

    #[test]
    fn exit_confirmation_timeout_proceeds_once() {
        let coordinator = ExitCoordinator::default();
        let request_id = match begin_exit_stop(&coordinator) {
            ExitRequestAction::Prompt(request_id) => request_id,
            action => panic!("expected prompt, got {action:?}"),
        };
        assert!(coordinator.timeout(request_id));
        assert!(!coordinator.timeout(request_id));
        assert!(!coordinator.confirm());
        assert_eq!(begin_exit_stop(&coordinator), ExitRequestAction::Wait);
    }

    #[test]
    fn backup_manifest_requires_and_verifies_every_core_archive() {
        let root = tempfile::tempdir().expect("backup root");
        let mut components = Vec::new();
        let mut hashes = BTreeMap::new();
        for component in BACKUP_COMPONENTS.iter().filter(|item| item.required) {
            let name = format!("{}.tar.gz", component.name);
            fs::write(root.path().join(&name), component.name).expect("archive fixture");
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
        read_backup_manifest(root.path()).expect("valid backup");

        fs::write(root.path().join("coordinator.tar.gz"), "corrupt").expect("corrupt archive");
        let error = read_backup_manifest(root.path()).expect_err("checksum must fail");
        assert!(error.contains("checksum failed"));
    }
}
