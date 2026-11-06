//! The host as a Linux service, with no desktop app. It makes its own token
//! and leaves it, with its port, in a file only the service's user can read.
//! The commands on the server (`commitarium-host status`) read that file, so
//! they reach the same API the app does and need no second way in. The
//! gateway is what other machines reach (`gateway.rs`).

use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use std::fs::{self, OpenOptions};
use std::io::{ErrorKind, Write};
use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::thread;

use crate::events::Bus;
use crate::gateway::{self, Device, Gateway, PairingOpened, Upstreams};
use crate::runtime::{block_on, spawn};
use crate::server::{self, Access, Launch, Shared};
use crate::{docker, jobs, process};

const CONNECTION_FILE: &str = "service.json";
/// Where the install script keeps a service's data.
pub const DEFAULT_DATA_DIR: &str = "/var/lib/commitarium";

/// How a command on the server reaches the running service.
#[derive(Deserialize, Serialize)]
struct Connection {
    port: u16,
    token: String,
}

/// The value after `flag`, if it is there.
pub fn option<'a>(args: &'a [String], flag: &str) -> Option<&'a str> {
    args.iter()
        .position(|arg| arg == flag)
        .and_then(|at| args.get(at + 1))
        .map(String::as_str)
}

/// The data directory from `--data-dir <path>`, or the service's own.
pub fn data_dir(args: &[String]) -> PathBuf {
    PathBuf::from(option(args, "--data-dir").unwrap_or(DEFAULT_DATA_DIR))
}

fn port_setting(name: &str, default: u16) -> u16 {
    std::env::var(name)
        .ok()
        .and_then(|port| port.parse().ok())
        .unwrap_or(default)
}

/// Runs until the service is told to stop. The stack stays up, as it does when
/// an app leaves: stopping it is the user's choice.
pub fn serve(data_dir: PathBuf) -> Result<(), String> {
    fs::create_dir_all(&data_dir).map_err(|e| format!("create the data directory: {e}"))?;
    fs::set_permissions(&data_dir, fs::Permissions::from_mode(0o700))
        .map_err(|e| format!("protect the data directory: {e}"))?;
    docker::prepare_runtime(&data_dir)?;

    let http = tiny_http::Server::http("127.0.0.1:0").map_err(|e| format!("listen: {e}"))?;
    let port = http
        .server_addr()
        .to_ip()
        .ok_or("the server has no IP address")?
        .port();
    let token = token()?;
    write_connection(
        &data_dir,
        &Connection {
            port,
            token: token.clone(),
        },
    )?;

    let gateway_token = token.clone();
    let gateway_dir = data_dir.clone();
    let shared = Arc::new(Shared::new(data_dir.clone(), Bus::default()));
    shared.begin_background_work();
    jobs::watch(shared.clone());
    let access = Access::new(
        Launch {
            token,
            origins: Vec::new(),
            data_dir: data_dir.clone(),
        },
        port,
    );
    let serving = shared.clone();
    thread::spawn(move || server::serve(http, access, serving));

    let gateway_port = port_setting("COMMITARIUM_GATEWAY_PORT", gateway::DEFAULT_PORT);
    let previews = shared.previews();
    let gateway = Gateway::open(
        &gateway_dir,
        Upstreams {
            host_port: port,
            host_token: gateway_token,
            coordinator_port: port_setting("COMMITARIUM_COORDINATOR_PORT", 8080),
            forgejo_port: port_setting("COMMITARIUM_FORGEJO_PORT", 3001),
            is_preview: Box::new(move |port| previews.serves_port(port)),
        },
    )?;
    shared.use_gateway(gateway.clone(), gateway_port);
    let listener = block_on(tokio::net::TcpListener::bind(("0.0.0.0", gateway_port)))
        .map_err(|e| format!("open port {gateway_port}: {e}"))?;
    spawn(async move {
        if let Err(error) = gateway.serve(listener).await {
            eprintln!("[host] the gateway stopped: {error}");
        }
    });

    let starting = shared.clone();
    thread::spawn(move || {
        starting.keep_trying_to_start_stack();
        // A deployment needs the coordinator for its source.
        starting.restore_deployments();
    });
    shared.keep_publishing();
    shared.keep_nightly_backups();
    crate::cleanup::keep_tidy(&data_dir, true);

    wait_for_stop()?;
    shared.stop_previews_and_logins();
    shared.deployments().stop_all();
    Ok(())
}

fn wait_for_stop() -> Result<(), String> {
    use tokio::signal::unix::{signal, SignalKind};
    block_on(async {
        let mut terminate =
            signal(SignalKind::terminate()).map_err(|e| format!("watch for stop: {e}"))?;
        let mut interrupt =
            signal(SignalKind::interrupt()).map_err(|e| format!("watch for stop: {e}"))?;
        tokio::select! {
            _ = terminate.recv() => {}
            _ = interrupt.recv() => {}
        }
        Ok(())
    })
}

fn token() -> Result<String, String> {
    let mut bytes = [0u8; 32];
    getrandom::fill(&mut bytes).map_err(|e| format!("make a host token: {e}"))?;
    Ok(bytes.iter().map(|byte| format!("{byte:02x}")).collect())
}

/// Replaces the file rather than rewriting it, so the mode always applies.
fn write_connection(data_dir: &Path, connection: &Connection) -> Result<(), String> {
    let path = data_dir.join(CONNECTION_FILE);
    match fs::remove_file(&path) {
        Err(error) if error.kind() != ErrorKind::NotFound => {
            return Err(format!("replace the connection file: {error}"));
        }
        _ => {}
    }
    let body = serde_json::to_vec(connection).map_err(|e| e.to_string())?;
    OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(&path)
        .and_then(|mut file| file.write_all(&body))
        .map_err(|e| format!("write the connection file: {e}"))
}

fn read_connection(data_dir: &Path) -> Result<Connection, String> {
    let path = data_dir.join(CONNECTION_FILE);
    let body = fs::read(&path).map_err(|error| match error.kind() {
        ErrorKind::PermissionDenied => "run this with sudo".to_string(),
        ErrorKind::NotFound => "the Commitarium service has not started yet".to_string(),
        _ => format!("read {}: {error}", path.display()),
    })?;
    serde_json::from_slice(&body).map_err(|_| "the connection file is not valid".to_string())
}

fn ask_service<T: DeserializeOwned>(
    connection: &Connection,
    method: reqwest::Method,
    path: &str,
    body: Option<serde_json::Value>,
) -> Result<T, String> {
    block_on(async {
        let client = reqwest::Client::builder()
            .no_proxy()
            .build()
            .map_err(|e| e.to_string())?;
        let mut request = client
            .request(
                method,
                format!("http://127.0.0.1:{}{path}", connection.port),
            )
            .bearer_auth(&connection.token);
        if let Some(body) = body {
            request = request.json(&body);
        }
        let answer = request
            .send()
            .await
            .map_err(|_| "the Commitarium service is not running".to_string())?;
        let failed = !answer.status().is_success();
        let body: serde_json::Value = answer
            .json()
            .await
            .map_err(|_| "the service sent an answer that could not be read".to_string())?;
        if failed {
            return Err(body["error"].as_str().unwrap_or("it failed").to_string());
        }
        serde_json::from_value(body)
            .map_err(|_| "the service sent an answer that could not be read".to_string())
    })
}

fn ask<T: DeserializeOwned>(connection: &Connection, path: &str) -> Result<T, String> {
    ask_service(connection, reqwest::Method::GET, path, None)
}

/// What `commitarium-host status` prints.
pub fn status(data_dir: &Path) -> Result<String, String> {
    let connection = read_connection(data_dir)?;
    let version: serde_json::Value = ask(&connection, "/v1/version")?;
    let stack: serde_json::Value = ask(&connection, "/v1/stack/status")?;
    Ok(format!(
        "Commitarium {}\n{}",
        version["version"].as_str().unwrap_or("unknown"),
        serde_json::to_string_pretty(&stack).map_err(|e| e.to_string())?,
    ))
}

/// Opens pairing and returns what `commitarium-host pair` prints.
pub fn pair(data_dir: &Path, address: Option<&str>) -> Result<String, String> {
    let connection = read_connection(data_dir)?;
    let opened: PairingOpened =
        ask_service(&connection, reqwest::Method::POST, "/v1/pairing/open", None)?;
    let (tailscale, home) = match address {
        Some(address) => (None, Some(address.to_string())),
        None => (tailscale_address(), outgoing_address()),
    };
    pairing_text(&opened, tailscale.as_deref(), home.as_deref())
        .ok_or("could not find this server's address; pass --address <address>".to_string())
}

/// The line to paste. A server on Tailscale gives two: its Tailscale address
/// works from anywhere with Tailscale on, its own only on its network.
fn pairing_text(
    opened: &PairingOpened,
    tailscale: Option<&str>,
    home: Option<&str>,
) -> Option<String> {
    let lines = match (tailscale, home) {
        (Some(tailscale), Some(home)) if tailscale != home => format!(
            "With Tailscale on that device, from anywhere:\n\n  {}\n\n\
             Or on this server's own network:\n\n  {}\n",
            pairing_line(tailscale, opened),
            pairing_line(home, opened),
        ),
        (Some(address), _) | (None, Some(address)) => {
            format!("  {}\n", pairing_line(address, opened))
        }
        (None, None) => return None,
    };
    Some(format!(
        "In Commitarium, choose Connect to a server and paste this.\n\
         It works once, for {} minutes:\n\n{lines}",
        opened.seconds / 60,
    ))
}

fn pairing_line(address: &str, opened: &PairingOpened) -> String {
    format!("commitarium://{address}:{}#{}", opened.port, opened.code)
}

fn tailscale_address() -> Option<String> {
    let output = process::command("tailscale")
        .args(["ip", "-4"])
        .output()
        .ok()?;
    let found = String::from_utf8(output.stdout).ok()?;
    let first = found.lines().next()?.trim();
    (output.status.success() && !first.is_empty()).then(|| first.to_string())
}

/// The address this machine uses to reach the internet. Nothing is sent. On a
/// VPS this is usually the public address; behind a router it is not, which is
/// what `--address` is for.
fn outgoing_address() -> Option<String> {
    let socket = std::net::UdpSocket::bind("0.0.0.0:0").ok()?;
    socket.connect("1.1.1.1:53").ok()?;
    Some(socket.local_addr().ok()?.ip().to_string())
}

/// What `commitarium-host devices` prints.
pub fn devices(data_dir: &Path) -> Result<String, String> {
    let connection = read_connection(data_dir)?;
    let devices: Vec<Device> = ask(&connection, "/v1/devices")?;
    if devices.is_empty() {
        return Ok("No paired devices.".to_string());
    }
    Ok(devices
        .iter()
        .map(|device| format!("{}  {}", &device.fingerprint[..16], device.name))
        .collect::<Vec<_>>()
        .join("\n"))
}

/// Revokes a device by its name or the fingerprint `devices` shows.
pub fn revoke(data_dir: &Path, device: &str) -> Result<(), String> {
    let connection = read_connection(data_dir)?;
    let devices: Vec<Device> = ask(&connection, "/v1/devices")?;
    let matching: Vec<&Device> = devices
        .iter()
        .filter(|known| known.name == device || known.fingerprint.starts_with(device))
        .collect();
    let [chosen] = matching[..] else {
        return Err(format!(
            "{} devices match {device}; use a fingerprint from: commitarium-host devices",
            matching.len()
        ));
    };
    let _: serde_json::Value = ask_service(
        &connection,
        reqwest::Method::POST,
        "/v1/devices/revoke",
        Some(serde_json::json!({ "device": chosen.fingerprint })),
    )?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_connection_file_is_private_and_replaced() {
        let dir = tempfile::tempdir().unwrap();
        for port in [1, 2] {
            write_connection(
                dir.path(),
                &Connection {
                    port,
                    token: "secret".into(),
                },
            )
            .unwrap();
        }
        let path = dir.path().join(CONNECTION_FILE);
        assert_eq!(
            fs::metadata(path).unwrap().permissions().mode() & 0o777,
            0o600
        );
        assert_eq!(read_connection(dir.path()).unwrap().port, 2);
    }

    #[test]
    fn a_service_not_started_says_so() {
        let dir = tempfile::tempdir().unwrap();
        assert_eq!(
            read_connection(dir.path()).err().unwrap(),
            "the Commitarium service has not started yet"
        );
    }

    #[test]
    fn the_data_directory_can_be_chosen() {
        assert_eq!(data_dir(&[]), Path::new(DEFAULT_DATA_DIR));
        let chosen = ["--data-dir".to_string(), "/srv/c".to_string()];
        assert_eq!(data_dir(&chosen), Path::new("/srv/c"));
    }

    #[test]
    fn a_server_on_tailscale_prints_both_lines() {
        let opened = PairingOpened {
            code: "ABCD2345".into(),
            port: 7443,
            seconds: 300,
        };
        let both = pairing_text(&opened, Some("100.64.0.1"), Some("192.168.1.20")).unwrap();
        assert!(both.contains("commitarium://100.64.0.1:7443#ABCD2345"));
        assert!(both.contains("commitarium://192.168.1.20:7443#ABCD2345"));
        let one = pairing_text(&opened, None, Some("192.168.1.20")).unwrap();
        assert!(one.contains("192.168.1.20") && !one.contains("anywhere"));
        assert!(pairing_text(&opened, None, None).is_none());
    }

    #[test]
    fn a_pairing_line_can_be_read_back() {
        let opened = PairingOpened {
            code: "ABCD2345".into(),
            port: 7443,
            seconds: 300,
        };
        let line = pairing_line("100.64.0.1", &opened);
        assert_eq!(
            crate::device_keys::parse_pairing_line(&line).unwrap(),
            ("100.64.0.1:7443".to_string(), "ABCD2345".to_string())
        );
    }
}
