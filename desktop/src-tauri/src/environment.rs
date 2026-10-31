//! The environment and validation commands. They run in the host (ADR-040).

use serde_json::{json, Value};
use tauri::AppHandle;

use crate::host;

#[tauri::command]
pub async fn provision_environment_request(
    app: AppHandle,
    request_id: String,
) -> Result<Value, String> {
    let body = json!({ "request_id": request_id });
    host::post(&app, "/v1/environment/provision", body).await
}

#[tauri::command]
pub async fn run_validation_job(app: AppHandle, job_id: String) -> Result<Value, String> {
    let body = json!({ "job_id": job_id });
    host::post(&app, "/v1/validation/run", body).await
}
