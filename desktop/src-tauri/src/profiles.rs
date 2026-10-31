//! The agent login commands. They run in the host (ADR-040). Login codes and
//! API keys pass through here on their way to it, and are never logged.

use serde_json::{json, Value};
use tauri::AppHandle;

use crate::host;

#[tauri::command]
pub async fn list_profiles(app: AppHandle) -> Result<Value, String> {
    host::get(&app, "/v1/profiles").await
}

#[tauri::command]
pub async fn begin_login(
    app: AppHandle,
    profile_id: String,
    method: String,
) -> Result<Value, String> {
    let body = json!({ "profile_id": profile_id, "method": method });
    host::post(&app, "/v1/profiles/login/begin", body).await
}

#[tauri::command]
pub async fn submit_login_code(
    app: AppHandle,
    profile_id: String,
    code: String,
) -> Result<Value, String> {
    let body = json!({ "profile_id": profile_id, "code": code });
    host::post(&app, "/v1/profiles/login/code", body).await
}

#[tauri::command]
pub async fn submit_api_key(
    app: AppHandle,
    profile_id: String,
    key: String,
) -> Result<Value, String> {
    let body = json!({ "profile_id": profile_id, "key": key });
    host::post(&app, "/v1/profiles/login/key", body).await
}

#[tauri::command]
pub async fn cancel_login(app: AppHandle, profile_id: String) -> Result<Value, String> {
    let body = json!({ "profile_id": profile_id });
    host::post(&app, "/v1/profiles/login/cancel", body).await
}

#[tauri::command]
pub async fn verify_profile(app: AppHandle, profile_id: String) -> Result<Value, String> {
    let body = json!({ "profile_id": profile_id });
    host::post(&app, "/v1/profiles/verify", body).await
}

#[tauri::command]
pub async fn disconnect_profile(app: AppHandle, profile_id: String) -> Result<Value, String> {
    let body = json!({ "profile_id": profile_id });
    host::post(&app, "/v1/profiles/disconnect", body).await
}
