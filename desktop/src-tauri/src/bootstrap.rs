//! The Forgejo viewer commands. The work is in `commitarium_host::bootstrap`.

use commitarium_host::bootstrap::{self, ForgejoViewerStatus};

#[tauri::command]
pub async fn get_forgejo_viewer_status() -> Result<ForgejoViewerStatus, String> {
    bootstrap::get_forgejo_viewer_status().await
}

#[tauri::command]
pub async fn configure_forgejo_viewer(password: String) -> Result<ForgejoViewerStatus, String> {
    bootstrap::configure_forgejo_viewer(password).await
}
