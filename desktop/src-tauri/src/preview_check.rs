//! The preview check command. It runs in the host (ADR-040).

use serde_json::{json, Value};
use tauri::AppHandle;

use crate::host;

#[tauri::command]
pub async fn run_preview_check(app: AppHandle, check_id: String) -> Result<Value, String> {
    let body = json!({ "check_id": check_id });
    host::post(&app, "/v1/preview-check/run", body).await
}
