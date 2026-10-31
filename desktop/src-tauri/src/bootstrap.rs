//! The Forgejo viewer commands. They run in the host (ADR-040).

use serde_json::{json, Value};
use tauri::AppHandle;

use crate::host;

#[tauri::command]
pub async fn get_forgejo_viewer_status(app: AppHandle) -> Result<Value, String> {
    host::get(&app, "/v1/forgejo/viewer").await
}

#[tauri::command]
pub async fn configure_forgejo_viewer(app: AppHandle, password: String) -> Result<Value, String> {
    let body = json!({ "password": password });
    host::post(&app, "/v1/forgejo/viewer", body).await
}
