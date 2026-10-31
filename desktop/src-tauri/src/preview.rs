//! The preview commands. The work is in `commitarium_host::preview`.

use commitarium_host::preview::{self, PreviewManager, PreviewStatus, PreviewTarget};
use tauri::{AppHandle, State};

use crate::host::events;

#[tauri::command]
pub async fn start_preview(
    app: AppHandle,
    manager: State<'_, PreviewManager>,
    project_id: String,
    target: PreviewTarget,
) -> Result<PreviewStatus, String> {
    preview::start_preview(events(&app), &manager, project_id, target).await
}

#[tauri::command]
pub async fn stop_preview(
    app: AppHandle,
    manager: State<'_, PreviewManager>,
    project_id: String,
    feature_id: Option<String>,
) -> Result<(), String> {
    preview::stop_preview(events(&app), &manager, project_id, feature_id).await
}

#[tauri::command]
pub async fn reset_preview_data(
    app: AppHandle,
    manager: State<'_, PreviewManager>,
    project_id: String,
) -> Result<(), String> {
    preview::reset_preview_data(events(&app), &manager, project_id).await
}

/// Removes the previews of the project's sessions that are no longer open:
/// their containers, networks, data, and images (ADR-019).
#[tauri::command]
pub async fn prune_session_previews(
    app: AppHandle,
    manager: State<'_, PreviewManager>,
    project_id: String,
    open_sessions: Vec<String>,
) -> Result<(), String> {
    preview::prune_session_previews(events(&app), &manager, project_id, open_sessions).await
}

#[tauri::command]
pub fn get_preview_status(
    manager: State<'_, PreviewManager>,
    project_id: String,
    feature_id: Option<String>,
) -> Result<Option<PreviewStatus>, String> {
    preview::get_preview_status(&manager, project_id, feature_id)
}

#[tauri::command]
pub async fn get_preview_logs(
    manager: State<'_, PreviewManager>,
    project_id: String,
    feature_id: Option<String>,
) -> Result<Vec<String>, String> {
    preview::get_preview_logs(&manager, project_id, feature_id).await
}
