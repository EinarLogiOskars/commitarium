//! The agent login commands. The work is in `commitarium_host::profiles`.

use commitarium_host::profiles::{self, Profile, ProfileManager};
use tauri::{AppHandle, State};

use crate::host::events;

#[tauri::command]
pub async fn list_profiles(manager: State<'_, ProfileManager>) -> Result<Vec<Profile>, String> {
    profiles::list_profiles(&manager).await
}

#[tauri::command]
pub fn begin_login(
    app: AppHandle,
    manager: State<'_, ProfileManager>,
    profile_id: String,
    method: String,
) -> Result<(), String> {
    profiles::begin_login(events(&app), &manager, profile_id, method)
}

#[tauri::command]
pub fn submit_login_code(
    manager: State<'_, ProfileManager>,
    profile_id: String,
    code: String,
) -> Result<(), String> {
    profiles::submit_login_code(&manager, profile_id, code)
}

#[tauri::command]
pub fn submit_api_key(
    app: AppHandle,
    manager: State<'_, ProfileManager>,
    profile_id: String,
    key: String,
) -> Result<(), String> {
    profiles::submit_api_key(events(&app), &manager, profile_id, key)
}

#[tauri::command]
pub fn cancel_login(
    app: AppHandle,
    manager: State<'_, ProfileManager>,
    profile_id: String,
) -> Result<(), String> {
    profiles::cancel_login(events(&app), &manager, profile_id)
}

#[tauri::command]
pub async fn verify_profile(
    manager: State<'_, ProfileManager>,
    profile_id: String,
) -> Result<Profile, String> {
    profiles::verify_profile(&manager, profile_id).await
}

#[tauri::command]
pub async fn disconnect_profile(
    manager: State<'_, ProfileManager>,
    profile_id: String,
) -> Result<Profile, String> {
    profiles::disconnect_profile(&manager, profile_id).await
}
