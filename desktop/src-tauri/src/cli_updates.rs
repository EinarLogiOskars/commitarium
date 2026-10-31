//! The agents' CLI status command. The background updater runs in the host
//! (ADR-040).

use serde_json::{json, Value};
use tauri::AppHandle;

use crate::host;

/// The agents' CLI versions and any update in progress. As ever this has an
/// answer, which is that nothing is known while the host cannot be reached.
#[tauri::command]
pub async fn agent_cli_status(app: AppHandle) -> Value {
    host::get(&app, "/v1/agents/cli-status")
        .await
        .unwrap_or_else(|_| json!([]))
}
