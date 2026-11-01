//! The host as a Linux service, with no desktop app. It makes its own token
//! and leaves it, with its port, in a file only the service's user can read.
//! The commands on the server (`commitarium-host status`) read that file, so
//! they reach the same API the app does and need no second way in.

use serde::{Deserialize, Serialize};
use std::fs::{self, OpenOptions};
use std::io::{ErrorKind, Write};
use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::thread;

use crate::events::Bus;
use crate::runtime::block_on;
use crate::server::{self, Access, Launch, Shared};
use crate::{docker, jobs};

const CONNECTION_FILE: &str = "service.json";
/// Where the install script keeps a service's data.
pub const DEFAULT_DATA_DIR: &str = "/var/lib/commitarium";

/// How a command on the server reaches the running service.
#[derive(Deserialize, Serialize)]
struct Connection {
    port: u16,
    token: String,
}

/// The data directory from `--data-dir <path>`, or the service's own.
pub fn data_dir(args: &[String]) -> Result<PathBuf, String> {
    match args {
        [] => Ok(PathBuf::from(DEFAULT_DATA_DIR)),
        [flag, path] if flag == "--data-dir" => Ok(PathBuf::from(path)),
        _ => Err("expected nothing, or --data-dir <path>".into()),
    }
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

    let shared = Arc::new(Shared::new(data_dir.clone(), Bus::default()));
    shared.begin_background_work();
    jobs::watch(shared.clone());
    let access = Access::new(
        Launch {
            token,
            origins: Vec::new(),
            data_dir,
        },
        port,
    );
    let serving = shared.clone();
    thread::spawn(move || server::serve(http, access, serving));
    let starting = shared.clone();
    thread::spawn(move || starting.keep_trying_to_start_stack());

    wait_for_stop()?;
    shared.stop_previews_and_logins();
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

fn ask_service(connection: &Connection, path: &str) -> Result<serde_json::Value, String> {
    block_on(async {
        let answer = reqwest::Client::builder()
            .no_proxy()
            .build()
            .map_err(|e| e.to_string())?
            .get(format!("http://127.0.0.1:{}{path}", connection.port))
            .bearer_auth(&connection.token)
            .send()
            .await
            .map_err(|_| "the Commitarium service is not running".to_string())?;
        answer
            .json()
            .await
            .map_err(|_| "the service sent an answer that could not be read".to_string())
    })
}

/// What `commitarium-host status` prints.
pub fn status(data_dir: &Path) -> Result<String, String> {
    let connection = read_connection(data_dir)?;
    let version = ask_service(&connection, "/v1/version")?;
    let stack = ask_service(&connection, "/v1/stack/status")?;
    Ok(format!(
        "Commitarium {}\n{}",
        version["version"].as_str().unwrap_or("unknown"),
        serde_json::to_string_pretty(&stack).map_err(|e| e.to_string())?,
    ))
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
        assert_eq!(data_dir(&[]).unwrap(), Path::new(DEFAULT_DATA_DIR));
        let chosen = ["--data-dir".to_string(), "/srv/c".to_string()];
        assert_eq!(data_dir(&chosen).unwrap(), Path::new("/srv/c"));
        assert!(data_dir(&["--other".to_string()]).is_err());
    }
}
