//! The preview commands. They run in the host (ADR-040).

use serde_json::{json, Value};
use tauri::AppHandle;

use crate::host;

#[tauri::command]
pub async fn start_preview(
    app: AppHandle,
    project_id: String,
    target: Value,
) -> Result<Value, String> {
    let body = json!({ "project_id": project_id, "target": target });
    host::post(&app, "/v1/previews/start", body).await
}

#[tauri::command]
pub async fn stop_preview(
    app: AppHandle,
    project_id: String,
    feature_id: Option<String>,
) -> Result<Value, String> {
    let body = json!({ "project_id": project_id, "feature_id": feature_id });
    host::post(&app, "/v1/previews/stop", body).await
}

#[tauri::command]
pub async fn reset_preview_data(app: AppHandle, project_id: String) -> Result<Value, String> {
    let body = json!({ "project_id": project_id });
    host::post(&app, "/v1/previews/reset", body).await
}

/// Removes the previews of the project's sessions that are no longer open:
/// their containers, networks, data, and images (ADR-019).
#[tauri::command]
pub async fn prune_session_previews(
    app: AppHandle,
    project_id: String,
    open_sessions: Vec<String>,
) -> Result<Value, String> {
    let body = json!({ "project_id": project_id, "open_sessions": open_sessions });
    host::post(&app, "/v1/previews/prune", body).await
}

#[tauri::command]
pub async fn get_preview_status(
    app: AppHandle,
    project_id: String,
    feature_id: Option<String>,
) -> Result<Value, String> {
    let body = json!({ "project_id": project_id, "feature_id": feature_id });
    host::post(&app, "/v1/previews/status", body).await
}

#[tauri::command]
pub async fn get_preview_logs(
    app: AppHandle,
    project_id: String,
    feature_id: Option<String>,
) -> Result<Value, String> {
    let body = json!({ "project_id": project_id, "feature_id": feature_id });
    host::post(&app, "/v1/previews/logs", body).await
}
