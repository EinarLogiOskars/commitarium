//! Which host the window shows: this computer's, or one of the paired servers
//! (ADR-040). Switching reloads the window, not the app, and the app opens on
//! the side last shown. The device's key lives in the OS keychain.

use commitarium_host::bridge::Bridge;
use commitarium_host::device_keys::{host_client, pair, Identity, PairedHost};
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::fs;
use std::path::PathBuf;
use std::sync::Mutex;
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
    /// Names the user gave servers, by fingerprint.
    #[serde(default)]
    pub names: BTreeMap<String, String>,
    /// Where each moved server was reached before, by fingerprint: the way
    /// back when its new address goes, as Tailscale's does when homelab
    /// mode is turned off.
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub previous: BTreeMap<String, String>,
}

impl Saved {
    fn active_host(&self) -> Option<&PairedHost> {
        let active = self.active.as_deref()?;
        self.hosts.iter().find(|host| host.fingerprint == active)
    }

    fn host(&self, fingerprint: &str) -> Option<&PairedHost> {
        self.hosts
            .iter()
            .find(|host| host.fingerprint == fingerprint)
    }

    fn forget(&mut self, fingerprint: &str) {
        self.hosts.retain(|host| host.fingerprint != fingerprint);
        self.names.remove(fingerprint);
        self.previous.remove(fingerprint);
        if self.active.as_deref() == Some(fingerprint) {
            self.active = None;
            self.mode = Mode::Local;
        }
    }

    /// Reaches a paired server at another address, keeping the one it had
    /// as the way back. Moving does not change which side is shown.
    fn moved(&mut self, fingerprint: &str, address: &str) -> Option<PairedHost> {
        let old = self.host(fingerprint)?.address.clone();
        if old != address {
            self.previous.insert(fingerprint.to_string(), old);
        }
        let moved = PairedHost {
            address: address.to_string(),
            fingerprint: fingerprint.to_string(),
        };
        let (mode, active) = (self.mode, self.active.clone());
        self.remember(moved.clone());
        self.mode = mode;
        self.active = active;
        Some(moved)
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

/// The device key, read from the keychain once per launch: each read can ask
/// the user for their password.
fn device_identity() -> Result<Identity, String> {
    static READ: Mutex<Option<Identity>> = Mutex::new(None);
    let mut read = READ
        .lock()
        .map_err(|_| "the device key is unavailable".to_string())?;
    if let Some(identity) = read.as_ref() {
        return Ok(identity.clone());
    }
    let identity = keychain_identity()?;
    *read = Some(identity.clone());
    Ok(identity)
}

fn keychain_identity() -> Result<Identity, String> {
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
        Err(error) => Err(format!(
            "macOS did not let Commitarium read this computer's key for your server from the \
             keychain. If a keychain prompt appears, enter your Mac password and choose Always \
             Allow, then Re-check. ({error})"
        )),
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
    coordinator_token: String,
    origins: Vec<String>,
) -> Result<Bridge, String> {
    let client = host_client(&device_identity()?, &host.fingerprint)?;
    Bridge::start(client, &host.address, token, coordinator_token, origins).await
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

/// Shows this computer. The window reloads after.
#[tauri::command]
pub fn use_this_computer(app: AppHandle) -> Result<(), String> {
    let mut saved = load(&app)?.unwrap_or_default();
    saved.mode = Mode::Local;
    save(&app, &saved)?;
    crate::host::show_this_computer(&app);
    Ok(())
}

/// Shows a paired server. The window reloads after.
#[tauri::command]
pub async fn use_server(app: AppHandle, fingerprint: String) -> Result<(), String> {
    let mut saved = load(&app)?.unwrap_or_default();
    let host = saved
        .host(&fingerprint)
        .cloned()
        .ok_or("this server is not paired")?;
    saved.mode = Mode::Remote;
    saved.active = Some(fingerprint);
    save(&app, &saved)?;
    crate::host::show_server(&app, &host).await;
    Ok(())
}

/// Pairs with a server and shows it. The window reloads after.
#[tauri::command]
pub async fn connect_to_server(app: AppHandle, line: String) -> Result<(), String> {
    let host = pair(&line, &device_name(), &device_identity()?).await?;
    let mut saved = load(&app)?.unwrap_or_default();
    saved.remember(host.clone());
    save(&app, &saved)?;
    // Paired again, perhaps at another address: an open bridge goes to the
    // old one.
    crate::host::drop_bridge(&app, &host.fingerprint);
    crate::host::show_server(&app, &host).await;
    Ok(())
}

#[derive(Serialize)]
pub struct Server {
    fingerprint: String,
    address: String,
    /// The user's name for it, else its address.
    name: String,
    /// Where it was reached before it moved, if it did.
    previous_address: Option<String>,
}

/// The sides the window can show: this computer and every paired server.
#[derive(Serialize)]
pub struct Places {
    servers: Vec<Server>,
    /// The shown server's fingerprint, or `None` for this computer.
    active: Option<String>,
}

#[tauri::command]
pub fn places(app: AppHandle) -> Result<Places, String> {
    let saved = load(&app)?.unwrap_or_default();
    Ok(places_of(&saved))
}

fn places_of(saved: &Saved) -> Places {
    Places {
        servers: saved
            .hosts
            .iter()
            .map(|host| Server {
                fingerprint: host.fingerprint.clone(),
                address: host.address.clone(),
                name: saved
                    .names
                    .get(&host.fingerprint)
                    .cloned()
                    .unwrap_or_else(|| host.address.clone()),
                previous_address: saved.previous.get(&host.fingerprint).cloned(),
            })
            .collect(),
        active: (saved.mode == Mode::Remote)
            .then(|| saved.active.clone())
            .flatten(),
    }
}

#[tauri::command]
pub fn rename_server(app: AppHandle, fingerprint: String, name: String) -> Result<(), String> {
    let mut saved = load(&app)?.unwrap_or_default();
    saved
        .host(&fingerprint)
        .ok_or("this server is not paired")?;
    match name.trim() {
        "" => saved.names.remove(&fingerprint),
        name => saved.names.insert(fingerprint, name.to_string()),
    };
    save(&app, &saved)
}

/// Reaches a paired server at another address from now on, such as its
/// Tailscale address in homelab mode (HOMELAB_PLAN.md, H4). It is checked
/// first: the same server answers there, known by its certificate, so
/// nothing is paired again. A shown server is shown from there at once.
#[tauri::command]
pub async fn move_server(
    app: AppHandle,
    fingerprint: String,
    address: String,
) -> Result<(), String> {
    let mut saved = load(&app)?.unwrap_or_default();
    saved
        .host(&fingerprint)
        .ok_or("this server is not paired")?;
    let address = address.trim().to_string();
    let client = host_client(&device_identity()?, &fingerprint)?;
    let answer = client
        .get(format!("https://{address}/host/v1/version"))
        .timeout(std::time::Duration::from_secs(10))
        .send()
        .await;
    let reached = match &answer {
        Ok(response) if response.status().is_success() => true,
        Ok(response) => {
            eprintln!("[remote] {address} answered {}", response.status());
            false
        }
        Err(error) => {
            eprintln!("[remote] could not reach {address}: {error:?}");
            false
        }
    };
    if !reached {
        // Tailscale's addresses are 100.64.0.0/10.
        let tailscale = address
            .split('.')
            .take(2)
            .map(str::parse::<u8>)
            .collect::<Result<Vec<_>, _>>()
            .is_ok_and(|parts| {
                parts.len() == 2 && parts[0] == 100 && (64..128).contains(&parts[1])
            });
        return Err(if tailscale {
            format!(
                "Commitarium could not reach your server at {address}. Make sure Tailscale is \
                 on and signed in on this computer, with the same account as the server, then \
                 try again."
            )
        } else {
            format!("Commitarium could not reach your server at {address} from this computer.")
        });
    }
    let shown = saved.mode == Mode::Remote && saved.active.as_deref() == Some(fingerprint.as_str());
    let moved = saved
        .moved(&fingerprint, &address)
        .ok_or("this server is not paired")?;
    save(&app, &saved)?;
    // The open bridge still goes to the old address.
    crate::host::drop_bridge(&app, &fingerprint);
    if shown {
        crate::host::show_server(&app, &moved).await;
    }
    Ok(())
}

/// Forgets a server on this device. The server still lists this device until
/// it is revoked there.
#[tauri::command]
pub fn forget_server(app: AppHandle, fingerprint: String) -> Result<(), String> {
    let mut saved = load(&app)?.unwrap_or_default();
    saved.forget(&fingerprint);
    save(&app, &saved)?;
    crate::host::close_server(&app, &fingerprint);
    Ok(())
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
    fn servers_show_their_name_or_address_and_the_shown_one() {
        let mut saved = Saved::default();
        saved.remember(host("1.1.1.1:7443", "one"));
        saved.remember(host("2.2.2.2:7443", "two"));
        saved.names.insert("one".into(), "Homelab".into());
        let places = places_of(&saved);
        let names: Vec<_> = places.servers.iter().map(|s| s.name.as_str()).collect();
        assert_eq!(names, ["Homelab", "2.2.2.2:7443"]);
        assert_eq!(places.active.as_deref(), Some("two"));

        saved.mode = Mode::Local;
        assert_eq!(places_of(&saved).active, None);
    }

    #[test]
    fn a_moved_server_keeps_its_way_back() {
        let mut saved = Saved::default();
        saved.remember(host("192.168.1.20:7443", "one"));
        saved.mode = Mode::Local;
        saved.moved("one", "100.101.102.103:7443").unwrap();
        // Moving shows nothing new.
        assert_eq!(saved.mode, Mode::Local);
        assert_eq!(saved.hosts[0].address, "100.101.102.103:7443");
        let places = places_of(&saved);
        assert_eq!(
            places.servers[0].previous_address.as_deref(),
            Some("192.168.1.20:7443")
        );
        assert!(saved.moved("two", "1.2.3.4:7443").is_none());
        saved.forget("one");
        assert!(saved.previous.is_empty());
    }

    #[test]
    fn forgetting_the_shown_server_shows_this_computer() {
        let mut saved = Saved::default();
        saved.remember(host("1.1.1.1:7443", "one"));
        saved.names.insert("one".into(), "Homelab".into());
        saved.forget("one");
        assert!(saved.hosts.is_empty() && saved.names.is_empty());
        assert_eq!(saved.mode, Mode::Local);
        assert_eq!(saved.active, None);
    }

    #[test]
    fn an_old_file_without_servers_is_local() {
        let saved: Saved = serde_json::from_str(r#"{"mode":"local"}"#).unwrap();
        assert_eq!(saved.mode, Mode::Local);
        assert!(saved.active_host().is_none());
    }
}
