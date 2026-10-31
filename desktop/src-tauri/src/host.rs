//! Starts and stops the bundled `commitarium-host` (ADR-040). The app makes a
//! token for each launch and gives it to the host over stdin, and only the
//! main window can ask for it: app windows stay on their narrow Tauri
//! commands (ADR-029). The host leaves by itself when that pipe closes.

use std::io::{BufRead, BufReader, Write};
use std::path::PathBuf;
use std::process::{Child, ChildStdin, Stdio};
use std::sync::{mpsc, Arc, Mutex};
use std::thread;
use std::time::Duration;

use commitarium_host::events::{Events, Frames, Sink};
use commitarium_host::server::{Launch, Ready};
use serde::Serialize;
use tauri::ipc::CapabilityBuilder;
use tauri::{AppHandle, Emitter, Manager, State};

const READY_TIMEOUT: Duration = Duration::from_secs(10);
/// How soon the app listens again after the host's stream ends.
const RELISTEN_AFTER: Duration = Duration::from_secs(1);

/// How the main window reaches its host.
#[derive(Clone, Debug, Serialize)]
pub struct HostConnection {
    url: String,
    token: String,
}

struct Running {
    child: Child,
    // Holding this open is what keeps the host alive.
    _stdin: ChildStdin,
    connection: HostConnection,
}

#[derive(Clone, Default)]
pub struct Host {
    running: Arc<Mutex<Option<Running>>>,
}

impl Host {
    /// The running host, starting it again if it has gone.
    fn connection(&self, app: &AppHandle) -> Result<HostConnection, String> {
        let mut running = self.running.lock().map_err(|_| "host state is poisoned")?;
        if let Some(host) = running.as_mut() {
            if matches!(host.child.try_wait(), Ok(None)) {
                return Ok(host.connection.clone());
            }
        }
        *running = None;
        let host = start(
            allowed_origins(app),
            crate::desktop_services::app_data_dir(app)?,
        )?;
        grant_main_window(app, &host.connection.url)?;
        let connection = host.connection.clone();
        *running = Some(host);
        Ok(connection)
    }

    /// The host if it is running. Listening never starts one, or it would
    /// bring back a host stopped for an update.
    fn running(&self) -> Option<HostConnection> {
        let mut running = self.running.lock().ok()?;
        let host = running.as_mut()?;
        matches!(host.child.try_wait(), Ok(None)).then(|| host.connection.clone())
    }

    /// Ends the host and waits for it, because Windows cannot replace a file
    /// that is still running.
    pub(crate) fn stop(&self) {
        let Ok(mut running) = self.running.lock() else {
            return;
        };
        if let Some(mut host) = running.take() {
            let _ = host.child.kill();
            let _ = host.child.wait();
        }
    }
}

/// Starts the host at launch. If it fails, the first call tries again and
/// reports why.
pub(crate) fn launch(app: &AppHandle) {
    if let Err(error) = app.state::<Host>().connection(app) {
        eprintln!("[host] {error}");
    }
}

/// Delivers updates as Tauri events, for host work still running in this
/// process.
struct TauriEvents(AppHandle);

impl Sink for TauriEvents {
    fn send(&self, name: &'static str, data: String) {
        let _ = self.0.emit_str(name, data);
    }
}

pub(crate) fn events(app: &AppHandle) -> Events {
    Events::new(TauriEvents(app.clone()))
}

/// Repeats the host's updates as Tauri events, so pages and app windows keep
/// listening the way they always have.
pub(crate) fn relay_events(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        let client = reqwest::Client::builder().no_proxy().build();
        let Ok(client) = client else {
            return;
        };
        loop {
            if let Some(connection) = app.state::<Host>().running() {
                let _ = relay(&app, &client, &connection).await;
            }
            tokio::time::sleep(RELISTEN_AFTER).await;
        }
    });
}

async fn relay(
    app: &AppHandle,
    client: &reqwest::Client,
    connection: &HostConnection,
) -> Result<(), reqwest::Error> {
    let mut stream = client
        .get(format!("{}/v1/events", connection.url))
        .bearer_auth(&connection.token)
        .send()
        .await?
        .error_for_status()?;
    let mut frames = Frames::default();
    while let Some(piece) = stream.chunk().await? {
        for (name, data) in frames.push(&piece) {
            if let Ok(payload) = serde_json::from_str::<serde_json::Value>(&data) {
                let _ = app.emit(&name, payload);
            }
        }
    }
    Ok(())
}

/// The bundler puts the host next to the app's own executable.
fn binary() -> Result<PathBuf, String> {
    let app = std::env::current_exe().map_err(|e| format!("find the app: {e}"))?;
    Ok(app.with_file_name(format!("commitarium-host{}", std::env::consts::EXE_SUFFIX)))
}

fn token() -> Result<String, String> {
    let mut bytes = [0u8; 32];
    getrandom::fill(&mut bytes).map_err(|e| format!("make a host token: {e}"))?;
    Ok(bytes.iter().map(|byte| format!("{byte:02x}")).collect())
}

fn start(origins: Vec<String>, data_dir: PathBuf) -> Result<Running, String> {
    let launch = Launch {
        token: token()?,
        origins,
        data_dir,
    };
    let mut child = crate::process::command(binary()?)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::inherit())
        .spawn()
        .map_err(|e| format!("start the host: {e}"))?;
    let mut stdin = child.stdin.take().ok_or("the host has no stdin")?;
    let stdout = child.stdout.take().ok_or("the host has no stdout")?;
    let line = serde_json::to_string(&launch).map_err(|e| e.to_string())?;
    let ready = writeln!(stdin, "{line}")
        .and_then(|()| stdin.flush())
        .map_err(|e| format!("give the host its token: {e}"))
        .and_then(|()| wait_until_ready(stdout));
    let ready = match ready {
        Ok(ready) => ready,
        Err(error) => {
            let _ = child.kill();
            let _ = child.wait();
            return Err(error);
        }
    };
    Ok(Running {
        child,
        _stdin: stdin,
        connection: HostConnection {
            url: format!("http://127.0.0.1:{}", ready.port),
            token: launch.token,
        },
    })
}

fn wait_until_ready(stdout: std::process::ChildStdout) -> Result<Ready, String> {
    let (sender, receiver) = mpsc::channel();
    thread::spawn(move || {
        let mut line = String::new();
        let read = BufReader::new(stdout).read_line(&mut line);
        let _ = sender.send(read.map(|_| line));
    });
    let line = receiver
        .recv_timeout(READY_TIMEOUT)
        .map_err(|_| "the host did not start in time".to_string())?
        .map_err(|e| format!("read from the host: {e}"))?;
    let ready: Ready =
        serde_json::from_str(&line).map_err(|_| "the host did not start".to_string())?;
    check_version(ready)
}

/// The app and its bundled host always ship together, so another version
/// means something left over.
fn check_version(ready: Ready) -> Result<Ready, String> {
    let expected = env!("CARGO_PKG_VERSION");
    if ready.version == expected {
        Ok(ready)
    } else {
        Err(format!(
            "the host is version {} but the app is {expected}",
            ready.version
        ))
    }
}

/// The main window's page, as each platform and a development build load it.
fn allowed_origins(app: &AppHandle) -> Vec<String> {
    let mut origins = vec![
        "tauri://localhost".to_string(),
        "http://tauri.localhost".to_string(),
    ];
    if cfg!(debug_assertions) {
        origins.extend(
            app.config()
                .build
                .dev_url
                .as_ref()
                .map(|url| url.origin().ascii_serialization()),
        );
    }
    origins
}

/// The HTTP plugin only reaches listed addresses, and the host's port is new
/// each launch.
fn grant_main_window(app: &AppHandle, url: &str) -> Result<(), String> {
    let port = url.rsplit(':').next().unwrap_or_default();
    app.add_capability(
        CapabilityBuilder::new(format!("host-{port}"))
            .window("main")
            .permission_scoped(
                "http:default",
                vec![serde_json::json!({ "url": format!("{url}/*") })],
                Vec::<serde_json::Value>::new(),
            ),
    )
    .map_err(|e| format!("reach the host: {e}"))
}

#[tauri::command]
pub async fn host_connection(
    app: AppHandle,
    host: State<'_, Host>,
) -> Result<HostConnection, String> {
    let host = host.inner().clone();
    tauri::async_runtime::spawn_blocking(move || host.connection(&app))
        .await
        .map_err(|_| "could not reach the host".to_string())?
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_leftover_host_of_another_version_is_refused() {
        let ready = |version: &str| Ready {
            port: 1,
            version: version.into(),
        };
        assert!(check_version(ready(env!("CARGO_PKG_VERSION"))).is_ok());
        assert!(check_version(ready("0.0.1")).is_err());
    }

    /// App windows must never get the token: they would then reach everything
    /// the host does, past the limits of ADR-029.
    #[test]
    fn only_the_main_window_can_ask_for_the_host() {
        let windows = include_str!("../permissions/windows.toml");
        let app_page = windows.split("identifier = \"app-page\"").nth(1).unwrap();
        assert!(!app_page.contains("allow-host-connection"));
        assert!(windows.contains("allow-host-connection"));
    }
}
