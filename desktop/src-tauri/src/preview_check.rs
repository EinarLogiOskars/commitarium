//! The preview check command. The work is in `commitarium_host::preview_check`.

use commitarium_host::preview_check::{self, PreviewCheckRun};

#[tauri::command]
pub async fn run_preview_check(check_id: String) -> Result<PreviewCheckRun, String> {
    preview_check::run_preview_check(check_id).await
}
