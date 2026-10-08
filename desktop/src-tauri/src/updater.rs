//! Native application-update discovery.
//!
//! The renderer receives release metadata only. Trusted endpoints, version
//! comparison, and signature policy remain in Tauri configuration so webview
//! code cannot redirect an update check to an arbitrary server.

use serde::Serialize;
use tauri::AppHandle;
use tauri_plugin_updater::{Update, UpdaterExt};

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

#[cfg(test)]
mod tests {
    use super::*;

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
}
