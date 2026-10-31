//! The Docker commands the renderer can reach. The work is in
//! `commitarium_host::docker`, except starting Docker Desktop, which only
//! makes sense on the user's own machine.

use std::sync::Arc;

use commitarium_host::cli_updates::CliUpdater;
use commitarium_host::docker::{self, docker_desktop_path};
use commitarium_host::profiles::ProfileManager;
use tauri::{AppHandle, Manager, State};

/// Install the release Compose definitions into the app's data directory.
pub(crate) fn prepare_runtime(app: &AppHandle) -> Result<(), String> {
    let data_dir = app
        .path()
        .app_data_dir()
        .map_err(|e| format!("resolve app data directory: {e}"))?;
    docker::prepare_runtime(&data_dir)
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
/// exact provider profile.
#[tauri::command]
pub async fn stack_up(
    manager: State<'_, ProfileManager>,
    updater: State<'_, Arc<CliUpdater>>,
) -> Result<Option<String>, String> {
    docker::stack_up(&manager, &updater).await
}

/// Tear the whole Commitarium stack down (project-wide, all profiles).
#[tauri::command]
pub async fn stack_down() -> Result<(), String> {
    docker::stack_down().await
}

/// Update the stack: pull the latest images, then recreate in the background.
#[tauri::command]
pub async fn stack_update(
    manager: State<'_, ProfileManager>,
    updater: State<'_, Arc<CliUpdater>>,
) -> Result<Option<String>, String> {
    docker::stack_update(&manager, &updater).await
}
