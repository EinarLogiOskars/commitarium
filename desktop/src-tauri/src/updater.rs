//! Native application-update discovery.
//!
//! The renderer receives release metadata only. Trusted endpoints, version
//! comparison, and signature policy remain in Tauri configuration so webview
//! code cannot redirect an update check to an arbitrary server.

use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Emitter, Manager, State};
use tauri_plugin_updater::{Update, UpdaterExt};

use crate::profiles;

pub const UPDATE_PROGRESS_EVENT: &str = "desktop-update-progress";

/// Serializes installation attempts and marks updater-driven restarts so the
/// normal quit confirmation cannot interrupt a completed installation.
#[derive(Default)]
pub struct UpdateCoordinator {
    busy: AtomicBool,
    restarting: AtomicBool,
}

struct UpdateOperation<'a> {
    coordinator: &'a UpdateCoordinator,
}

impl<'a> UpdateOperation<'a> {
    fn begin(coordinator: &'a UpdateCoordinator) -> Result<Self, String> {
        coordinator
            .busy
            .compare_exchange(false, true, Ordering::AcqRel, Ordering::Acquire)
            .map_err(|_| "an application update is already in progress".to_string())?;
        Ok(Self { coordinator })
    }
}

impl Drop for UpdateOperation<'_> {
    fn drop(&mut self) {
        self.coordinator.busy.store(false, Ordering::Release);
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Deserialize, Serialize)]
pub struct RunningWorkOrder {
    project_id: String,
    project_name: String,
    feature_id: String,
    feature_title: String,
    run_id: String,
    updated_at: String,
}

#[derive(Debug, Deserialize)]
struct AttentionSnapshot {
    #[serde(default)]
    running: Vec<RunningWorkOrder>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(tag = "status", rename_all = "snake_case")]
pub enum InstallDesktopUpdateResult {
    Blocked { work_orders: Vec<RunningWorkOrder> },
    Installing { version: String },
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(tag = "status", rename_all = "snake_case")]
pub enum DesktopUpdateProgress {
    Downloading {
        version: String,
        downloaded_bytes: u64,
        total_bytes: Option<u64>,
    },
    Installing {
        version: String,
    },
    Restarting {
        version: String,
    },
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct DesktopUpdateInfo {
    version: String,
    notes: Option<String>,
    published_at: Option<String>,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
#[serde(tag = "status", rename_all = "snake_case")]
pub enum DesktopUpdateCheck {
    UpToDate {
        current_version: String,
    },
    Available {
        current_version: String,
        update: DesktopUpdateInfo,
    },
}

fn update_check_response(current_version: String, update: Option<Update>) -> DesktopUpdateCheck {
    match update {
        Some(update) => DesktopUpdateCheck::Available {
            current_version,
            update: DesktopUpdateInfo {
                version: update.version,
                notes: update.body,
                published_at: update.date.map(|date| date.to_string()),
            },
        },
        None => DesktopUpdateCheck::UpToDate { current_version },
    }
}

#[tauri::command]
pub async fn check_desktop_update(app: AppHandle) -> Result<DesktopUpdateCheck, String> {
    let current_version = app.package_info().version.to_string();
    let updater = app
        .updater()
        .map_err(|error| format!("desktop updater is unavailable: {error}"))?;
    let update = updater
        .check()
        .await
        .map_err(|error| format!("could not check for updates: {error}"))?;
    Ok(update_check_response(current_version, update))
}

fn coordinator_url(path: &str) -> String {
    let port = std::env::var("COMMITARIUM_COORDINATOR_PORT").unwrap_or_else(|_| "8080".into());
    format!("http://127.0.0.1:{port}{path}")
}

async fn running_work_orders() -> Result<Vec<RunningWorkOrder>, String> {
    let client = reqwest::Client::builder()
        .timeout(Duration::from_secs(5))
        .build()
        .map_err(|error| format!("could not prepare the update safety check: {error}"))?;
    let response = client
        .get(coordinator_url("/api/v1/attention"))
        .send()
        .await
        .map_err(|error| {
            format!("could not confirm whether work orders are running; try again: {error}")
        })?;
    if !response.status().is_success() {
        return Err(format!(
            "could not confirm whether work orders are running; coordinator returned HTTP {}",
            response.status()
        ));
    }
    response
        .json::<AttentionSnapshot>()
        .await
        .map(|snapshot| snapshot.running)
        .map_err(|error| format!("could not read the active work-order status; try again: {error}"))
}

fn blocked_result(work_orders: Vec<RunningWorkOrder>) -> Option<InstallDesktopUpdateResult> {
    (!work_orders.is_empty()).then_some(InstallDesktopUpdateResult::Blocked { work_orders })
}

fn verify_expected_version(expected: &str, actual: &str) -> Result<(), String> {
    if expected == actual {
        Ok(())
    } else {
        Err(format!(
            "the available update changed from {expected} to {actual}; check for updates again"
        ))
    }
}

#[tauri::command]
pub async fn install_desktop_update(
    app: AppHandle,
    coordinator: State<'_, UpdateCoordinator>,
    expected_version: String,
) -> Result<InstallDesktopUpdateResult, String> {
    let expected_version = expected_version.trim();
    if expected_version.is_empty() {
        return Err("the expected update version is required".into());
    }

    let _operation = UpdateOperation::begin(&coordinator)?;
    let shutdown_profiles = app.state::<profiles::ProfileManager>().inner().clone();
    let before_exit_app = app.clone();
    let updater = app
        .updater_builder()
        .on_before_exit(move || {
            shutdown_profiles.shutdown();
            before_exit_app.cleanup_before_exit();
        })
        .build()
        .map_err(|error| format!("desktop updater is unavailable: {error}"))?;
    let update = updater
        .check()
        .await
        .map_err(|error| format!("could not check for updates: {error}"))?
        .ok_or_else(|| "no application update is currently available".to_string())?;
    verify_expected_version(expected_version, &update.version)?;

    if let Some(blocked) = blocked_result(running_work_orders().await?) {
        return Ok(blocked);
    }

    let version = update.version.clone();
    let progress_app = app.clone();
    let progress_version = version.clone();
    let mut downloaded_bytes = 0_u64;
    let bytes = update
        .download(
            move |chunk_size, total_bytes| {
                downloaded_bytes = downloaded_bytes.saturating_add(chunk_size as u64);
                let _ = progress_app.emit(
                    UPDATE_PROGRESS_EVENT,
                    DesktopUpdateProgress::Downloading {
                        version: progress_version.clone(),
                        downloaded_bytes,
                        total_bytes,
                    },
                );
            },
            || {},
        )
        .await
        .map_err(|error| format!("could not download the signed update: {error}"))?;

    // A run can begin while the package downloads. Re-check immediately before
    // installation so an updater can never interrupt a newly active provider.
    if let Some(blocked) = blocked_result(running_work_orders().await?) {
        return Ok(blocked);
    }

    let _ = app.emit(
        UPDATE_PROGRESS_EVENT,
        DesktopUpdateProgress::Installing {
            version: version.clone(),
        },
    );
    coordinator.restarting.store(true, Ordering::Release);
    if let Err(error) = update.install(bytes) {
        coordinator.restarting.store(false, Ordering::Release);
        return Err(format!("could not install the signed update: {error}"));
    }

    let _ = app.emit(
        UPDATE_PROGRESS_EVENT,
        DesktopUpdateProgress::Restarting {
            version: version.clone(),
        },
    );
    app.request_restart();
    Ok(InstallDesktopUpdateResult::Installing { version })
}

pub fn is_restarting(app: &AppHandle) -> bool {
    app.state::<UpdateCoordinator>()
        .restarting
        .load(Ordering::Acquire)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn running_work_order() -> RunningWorkOrder {
        RunningWorkOrder {
            project_id: "project-1".into(),
            project_name: "Commitarium".into(),
            feature_id: "feature-1".into(),
            feature_title: "Safe updates".into(),
            run_id: "run-1".into(),
            updated_at: "2026-10-08T12:00:00Z".into(),
        }
    }

    #[test]
    fn up_to_date_response_has_stable_tagged_shape() {
        let response = DesktopUpdateCheck::UpToDate {
            current_version: "0.4.1".into(),
        };
        assert_eq!(
            serde_json::to_value(response).unwrap(),
            serde_json::json!({
                "status": "up_to_date",
                "current_version": "0.4.1"
            })
        );
    }

    #[test]
    fn available_response_has_stable_tagged_shape() {
        let response = DesktopUpdateCheck::Available {
            current_version: "0.4.1".into(),
            update: DesktopUpdateInfo {
                version: "0.4.2".into(),
                notes: Some("Safer recovery".into()),
                published_at: Some("2026-10-08 12:00:00.0 +00:00:00".into()),
            },
        };
        assert_eq!(
            serde_json::to_value(response).unwrap(),
            serde_json::json!({
                "status": "available",
                "current_version": "0.4.1",
                "update": {
                    "version": "0.4.2",
                    "notes": "Safer recovery",
                    "published_at": "2026-10-08 12:00:00.0 +00:00:00"
                }
            })
        );
    }

    #[test]
    fn running_work_orders_block_installation_with_identifying_details() {
        let result = blocked_result(vec![running_work_order()]).unwrap();
        assert_eq!(
            serde_json::to_value(result).unwrap(),
            serde_json::json!({
                "status": "blocked",
                "work_orders": [{
                    "project_id": "project-1",
                    "project_name": "Commitarium",
                    "feature_id": "feature-1",
                    "feature_title": "Safe updates",
                    "run_id": "run-1",
                    "updated_at": "2026-10-08T12:00:00Z"
                }]
            })
        );
    }

    #[test]
    fn no_running_work_orders_allows_installation_to_continue() {
        assert_eq!(blocked_result(Vec::new()), None);
    }

    #[test]
    fn stale_update_selection_is_rejected() {
        let error = verify_expected_version("0.4.1", "0.4.2").unwrap_err();
        assert!(error.contains("changed from 0.4.1 to 0.4.2"));
        assert_eq!(verify_expected_version("0.4.2", "0.4.2"), Ok(()));
    }

    #[test]
    fn only_one_update_operation_can_run_at_a_time() {
        let coordinator = UpdateCoordinator::default();
        let operation = UpdateOperation::begin(&coordinator).unwrap();
        assert!(UpdateOperation::begin(&coordinator).is_err());
        drop(operation);
        assert!(UpdateOperation::begin(&coordinator).is_ok());
    }

    #[test]
    fn download_progress_has_stable_tagged_shape() {
        let progress = DesktopUpdateProgress::Downloading {
            version: "0.4.2".into(),
            downloaded_bytes: 1024,
            total_bytes: Some(4096),
        };
        assert_eq!(
            serde_json::to_value(progress).unwrap(),
            serde_json::json!({
                "status": "downloading",
                "version": "0.4.2",
                "downloaded_bytes": 1024,
                "total_bytes": 4096
            })
        );
    }
}
