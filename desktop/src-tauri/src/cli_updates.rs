//! The agents' CLI status command, and starting the background updater with
//! the app. The work is in `commitarium_host::cli_updates`.

use std::sync::Arc;

use commitarium_host::cli_updates::{self, CliStatus, CliUpdater};
use commitarium_host::profiles::ProfileManager;
use tauri::{AppHandle, Manager, State};

/// The agents' CLI versions and any update in progress.
#[tauri::command]
pub fn agent_cli_status(updater: State<'_, Arc<CliUpdater>>) -> Vec<CliStatus> {
    cli_updates::agent_cli_status(&updater)
}

/// Start the background updater. It never blocks startup.
pub(crate) fn spawn(app: AppHandle) {
    cli_updates::spawn(
        app.state::<Arc<CliUpdater>>().inner().clone(),
        app.state::<ProfileManager>().inner().clone(),
        crate::host::events(&app),
    );
}
