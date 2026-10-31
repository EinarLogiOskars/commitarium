//! The environment and validation commands. The work is in
//! `commitarium_host::environment`.

use commitarium_host::environment::{self, EnvironmentProvisionResult, ValidationRunResult};
use commitarium_host::profiles::ProfileManager;
use tauri::State;

#[tauri::command]
pub async fn provision_environment_request(
    request_id: String,
    manager: State<'_, ProfileManager>,
) -> Result<EnvironmentProvisionResult, String> {
    environment::provision_environment_request(request_id, &manager).await
}

#[tauri::command]
pub async fn run_validation_job(job_id: String) -> Result<ValidationRunResult, String> {
    environment::run_validation_job(job_id).await
}
