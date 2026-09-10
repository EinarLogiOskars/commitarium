//! Minimal host-durable UI-local store.
//!
//! Persists a single opaque JSON blob in the app's data directory so UI-local
//! state survives restarts. Feature discovery does not depend on this (the
//! backend feature-list endpoint is settled); its long-term job is UI-local
//! game/workshop state. Kept intentionally tiny for Slice 1 — the frame is
//! what matters.

use serde_json::Value;
use std::fs;
use std::path::PathBuf;
use tauri::{AppHandle, Manager};

const STATE_FILE: &str = "ui-state.json";

fn state_path(app: &AppHandle) -> Result<PathBuf, String> {
    let dir = app
        .path()
        .app_data_dir()
        .map_err(|e| format!("resolve app data dir: {e}"))?;
    fs::create_dir_all(&dir).map_err(|e| format!("create app data dir: {e}"))?;
    Ok(dir.join(STATE_FILE))
}

/// Load the persisted UI-local state, or `null` when nothing is stored yet.
#[tauri::command]
pub fn load_ui_state(app: AppHandle) -> Result<Value, String> {
    let path = state_path(&app)?;
    match fs::read_to_string(&path) {
        Ok(contents) => {
            serde_json::from_str(&contents).map_err(|e| format!("parse ui state: {e}"))
        }
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Value::Null),
        Err(e) => Err(format!("read ui state: {e}")),
    }
}

/// Replace the persisted UI-local state with `state`.
#[tauri::command]
pub fn save_ui_state(app: AppHandle, state: Value) -> Result<(), String> {
    let path = state_path(&app)?;
    let contents = serde_json::to_string_pretty(&state).map_err(|e| format!("encode ui state: {e}"))?;
    fs::write(&path, contents).map_err(|e| format!("write ui state: {e}"))
}
