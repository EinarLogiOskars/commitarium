//! Which host this app uses: its own, or a paired server (ADR-040). The choice
//! is read once at launch, because everything else follows from it, and
//! changing it restarts the app. The device's key lives in the OS keychain.

use commitarium_host::bridge::Bridge;
use commitarium_host::device_keys::{host_client, pair, Identity, PairedHost};
use serde::{Deserialize, Serialize};
use std::fs;
use std::path::PathBuf;
use tauri::AppHandle;

const FILE: &str = "connection.json";
/// Written by any install that has already run, so an update asks nothing.
const EARLIER_USE: &str = "ui-state.json";
const KEYCHAIN_SERVICE: &str = "Commitarium";
const KEYCHAIN_ENTRY: &str = "device-key";

#[derive(Clone, Copy, Debug, Default, Deserialize, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Mode {
    #[default]
    Local,
    Remote,
}

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
pub struct Saved {
    pub mode: Mode,
    #[serde(default)]
    pub hosts: Vec<PairedHost>,
    #[serde(default)]
    pub active: Option<String>,
}

impl Saved {
    fn active_host(&self) -> Option<&PairedHost> {
        let active = self.active.as_deref()?;
        self.hosts.iter().find(|host| host.fingerprint == active)
    }

    fn remember(&mut self, host: PairedHost) {
        self.mode = Mode::Remote;
        self.active = Some(host.fingerprint.clone());
        self.hosts
            .retain(|known| known.fingerprint != host.fingerprint);
        self.hosts.push(host);
    }
}

fn path(app: &AppHandle) -> Result<PathBuf, String> {
    Ok(crate::desktop_services::app_data_dir(app)?.join(FILE))
}

fn load(app: &AppHandle) -> Result<Option<Saved>, String> {
    match fs::read(path(app)?) {
        Ok(body) => serde_json::from_slice(&body)
            .map(Some)
            .map_err(|_| "the saved connection could not be read".to_string()),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(error) => Err(format!("read the saved connection: {error}")),
    }
}

fn save(app: &AppHandle, saved: &Saved) -> Result<(), String> {
    let body = serde_json::to_vec_pretty(saved).map_err(|e| e.to_string())?;
    fs::write(path(app)?, body).map_err(|e| format!("save the connection: {e}"))
}

fn device_identity() -> Result<Identity, String> {
    let entry = keyring::Entry::new(KEYCHAIN_SERVICE, KEYCHAIN_ENTRY)
        .map_err(|e| format!("open the keychain: {e}"))?;
    match entry.get_password() {
        Ok(stored) => {
            serde_json::from_str(&stored).map_err(|_| "the device key is damaged".to_string())
        }
        Err(keyring::Error::NoEntry) => {
            let identity = Identity::generate()?;
            let stored = serde_json::to_string(&identity).map_err(|e| e.to_string())?;
            entry
                .set_password(&stored)
                .map_err(|e| format!("save the device key: {e}"))?;
            Ok(identity)
        }
        Err(error) => Err(format!("read the keychain: {error}")),
    }
}

fn device_name() -> String {
    crate::process::command("hostname")
        .output()
        .ok()
        .and_then(|output| String::from_utf8(output.stdout).ok())
        .map(|name| name.trim().to_string())
        .filter(|name| !name.is_empty())
        .unwrap_or_else(|| "This device".to_string())
}

/// The active server, when this launch is in remote mode.
pub(crate) fn active_host(app: &AppHandle) -> Result<Option<PairedHost>, String> {
    Ok(load(app)?
        .filter(|saved| saved.mode == Mode::Remote)
        .and_then(|saved| saved.active_host().cloned()))
}

pub(crate) async fn open_bridge(
    host: &PairedHost,
    token: String,
    origins: Vec<String>,
) -> Result<Bridge, String> {
    let client = host_client(&device_identity()?, &host.fingerprint)?;
    Bridge::start(client, &host.address, token, origins).await
}

/// What the page needs before it shows anything.
#[derive(Serialize)]
pub struct ConnectionMode {
    /// `None` on a first launch, which asks.
    mode: Option<Mode>,
    server: Option<String>,
}

#[tauri::command]
pub fn connection_mode(app: AppHandle) -> Result<ConnectionMode, String> {
    let saved = load(&app)?;
    let used_before = crate::desktop_services::app_data_dir(&app)?
        .join(EARLIER_USE)
        .exists();
    Ok(ConnectionMode {
        mode: saved
            .as_ref()
            .map(|saved| saved.mode)
            .or(used_before.then_some(Mode::Local)),
        server: saved
            .as_ref()
            .filter(|saved| saved.mode == Mode::Remote)
            .and_then(Saved::active_host)
            .map(|host| host.address.clone()),
    })
}

#[tauri::command]
pub fn use_this_computer(app: AppHandle) -> Result<(), String> {
    let mut saved = load(&app)?.unwrap_or_default();
    saved.mode = Mode::Local;
    save(&app, &saved)
}

/// Pairs with a server and makes it the one in use, from the next launch.
#[tauri::command]
pub async fn connect_to_server(app: AppHandle, line: String) -> Result<(), String> {
    let host = pair(&line, &device_name(), &device_identity()?).await?;
    let mut saved = load(&app)?.unwrap_or_default();
    saved.remember(host);
    save(&app, &saved)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn host(address: &str, fingerprint: &str) -> PairedHost {
        PairedHost {
            address: address.into(),
            fingerprint: fingerprint.into(),
        }
    }

    #[test]
    fn a_server_paired_again_replaces_its_old_address() {
        let mut saved = Saved::default();
        saved.remember(host("1.1.1.1:7443", "abc"));
        saved.remember(host("2.2.2.2:7443", "abc"));
        assert_eq!(saved.hosts.len(), 1);
        assert_eq!(saved.mode, Mode::Remote);
        assert_eq!(saved.active_host().unwrap().address, "2.2.2.2:7443");
    }

    #[test]
    fn an_old_file_without_servers_is_local() {
        let saved: Saved = serde_json::from_str(r#"{"mode":"local"}"#).unwrap();
        assert_eq!(saved.mode, Mode::Local);
        assert!(saved.active_host().is_none());
    }
}
