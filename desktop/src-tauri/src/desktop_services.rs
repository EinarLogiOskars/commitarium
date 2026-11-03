//! Trusted desktop services: settings, exit, notifications, disk space, and
//! backups.
//!
//! The renderer can manage typed presentation preferences, submit a bounded
//! notification, inspect local disk readiness, and select a backup directory.
//! It cannot choose Docker images, containers, volume paths, archive members,
//! or commands.

use serde::{Deserialize, Serialize};
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tauri::{AppHandle, Emitter, Manager, State};
use tauri_plugin_notification::NotificationExt;

use commitarium_host::backup::{self, BackupInspection, BackupResult};

const SETTINGS_FILE: &str = "desktop-settings.json";
const NOTIFICATION_LEDGER_FILE: &str = "notification-ledger.json";
const MAX_NOTIFICATION_IDS: usize = 2_000;
pub const EXIT_CONFIRMATION_EVENT: &str = "exit-confirmation-requested";
pub const EXIT_CONFIRMATION_TIMEOUT: Duration = Duration::from_secs(30);
/// How long the window has to show the quit question before quitting goes on
/// without it.
const EXIT_PROMPT_PATIENCE: Duration = Duration::from_secs(3);

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

const BACKUP_PROGRESS_EVENT: &str = "backup_progress";
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

pub struct ExitCoordinator {
    state: Mutex<ExitCoordinatorState>,
}

#[derive(Debug)]
struct ExitCoordinatorState {
    phase: ExitPhase,
    next_request_id: u64,
    /// The last request the window said it shows.
    shown: Option<u64>,
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
                shown: None,
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

    fn shown(&self) {
        let mut state = self.state.lock().unwrap_or_else(|error| error.into_inner());
        if let ExitPhase::Pending(request_id) = state.phase {
            state.shown = Some(request_id);
        }
    }

    /// A window that cannot show the question, such as a blank one, must not
    /// keep the app from quitting.
    fn unshown(&self, request_id: u64) -> bool {
        let mut state = self.state.lock().unwrap_or_else(|error| error.into_inner());
        if state.phase == ExitPhase::Pending(request_id) && state.shown != Some(request_id) {
            state.phase = ExitPhase::ShuttingDown;
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

pub(crate) fn app_data_dir(app: &AppHandle) -> Result<PathBuf, String> {
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
pub async fn create_backup(
    app: AppHandle,
    backup: State<'_, BackupCoordinator>,
    destination: String,
) -> Result<BackupResult, String> {
    let app = app.clone();
    let backup = backup.inner().clone();
    tauri::async_runtime::spawn_blocking(move || {
        let _guard = backup
            .operation
            .lock()
            .map_err(|_| "backup coordinator is unavailable".to_string())?;
        with_context(&app, |ctx| backup::create(ctx, Path::new(&destination)))
    })
    .await
    .map_err(|_| "backup worker stopped unexpectedly".to_string())?
}

#[tauri::command]
pub async fn restore_backup(
    app: AppHandle,
    backup: State<'_, BackupCoordinator>,
    source: String,
) -> Result<BackupResult, String> {
    let app = app.clone();
    let backup = backup.inner().clone();
    tauri::async_runtime::spawn_blocking(move || {
        let _guard = backup
            .operation
            .lock()
            .map_err(|_| "backup coordinator is unavailable".to_string())?;
        with_context(&app, |ctx| backup::restore(ctx, Path::new(&source)))
    })
    .await
    .map_err(|_| "restore worker stopped unexpectedly".to_string())?
}

/// The backup code's view of this app: its data directory and files, its
/// progress events, and the host that brings the stack up, since that reads
/// its logins.
fn with_context<T>(
    app: &AppHandle,
    work: impl FnOnce(&backup::Context) -> Result<T, String>,
) -> Result<T, String> {
    let progress = |step: &str| {
        let _ = app.emit(BACKUP_PROGRESS_EVENT, BackupProgress { step });
    };
    let start_stack =
        || crate::host::post_blocking(app, "/v1/stack/up", serde_json::Value::Null).map(|_| ());
    work(&backup::Context {
        state_dir: app_data_dir(app)?,
        state_files: APP_STATE_FILES,
        progress: &progress,
        start_stack: &start_stack,
    })
}

#[derive(Clone, Serialize)]
struct BackupProgress<'a> {
    step: &'a str,
}

#[tauri::command]
pub async fn inspect_backup(source: String) -> Result<BackupInspection, String> {
    tauri::async_runtime::spawn_blocking(move || {
        backup::inspect(Path::new(&source), APP_STATE_FILES)
    })
    .await
    .map_err(|_| "backup inspection worker stopped unexpectedly".to_string())?
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
pub fn confirm_exit(app: AppHandle, coordinator: State<'_, ExitCoordinator>) -> bool {
    if !coordinator.confirm() {
        return false;
    }
    launch_exit_shutdown(app);
    true
}

#[tauri::command]
pub fn cancel_exit(coordinator: State<'_, ExitCoordinator>) -> bool {
    coordinator.cancel()
}

/// The window says it shows the quit question.
#[tauri::command]
pub fn exit_prompt_shown(coordinator: State<'_, ExitCoordinator>) {
    coordinator.shown();
}

pub fn schedule_exit_timeout(app: AppHandle, request_id: u64) {
    let unanswered = app.clone();
    std::thread::spawn(move || {
        std::thread::sleep(EXIT_PROMPT_PATIENCE);
        if unanswered
            .state::<ExitCoordinator>()
            .inner()
            .unshown(request_id)
        {
            launch_exit_shutdown(unanswered);
        }
    });
    std::thread::spawn(move || {
        std::thread::sleep(EXIT_CONFIRMATION_TIMEOUT);
        if app.state::<ExitCoordinator>().inner().timeout(request_id) {
            launch_exit_shutdown(app);
        }
    });
}

fn launch_exit_shutdown(app: AppHandle) {
    std::thread::spawn(move || {
        crate::host::stop_logins_and_stack(&app);
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
    fn quitting_goes_on_when_the_window_cannot_ask() {
        let coordinator = ExitCoordinator::default();
        let ExitRequestAction::Prompt(blank) = begin_exit_stop(&coordinator) else {
            panic!("expected prompt");
        };
        assert!(coordinator.unshown(blank));
        assert_eq!(begin_exit_stop(&coordinator), ExitRequestAction::Wait);

        let coordinator = ExitCoordinator::default();
        let ExitRequestAction::Prompt(asked) = begin_exit_stop(&coordinator) else {
            panic!("expected prompt");
        };
        coordinator.shown();
        assert!(!coordinator.unshown(asked));
        assert!(coordinator.confirm());
    }
}
