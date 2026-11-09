//! Starts, stops and talks to the bundled `commitarium-host` (ADR-040). The
//! app makes a token for each launch and gives it to the host over stdin, and
//! only the main window can ask for it: app windows stay on their narrow Tauri
//! commands (ADR-029), which forward here. The host leaves by itself when that
//! pipe closes.

use std::collections::{BTreeMap, BTreeSet};
use std::future::Future;
use std::io::{BufRead, BufReader, Write};
use std::path::PathBuf;
use std::process::{Child, ChildStdin, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::thread;
use std::time::Duration;

use commitarium_host::bridge::Bridge;
use commitarium_host::device_keys::PairedHost;
use commitarium_host::events::Frames;
use commitarium_host::server::{Launch, Ready};
use commitarium_host::{docker, preview, profiles};
use reqwest::Method;
use serde::Serialize;
use serde_json::{json, Value};
use tauri::ipc::CapabilityBuilder;
use tauri::{AppHandle, Emitter, Manager};

const READY_TIMEOUT: Duration = Duration::from_secs(10);
/// How soon the app listens again after the host's stream ends.
const RELISTEN_AFTER: Duration = Duration::from_secs(1);
/// The host speaks at least every 15 seconds, so this much silence means a
/// connection the network dropped without saying so.
const SILENCE_LIMIT: Duration = Duration::from_secs(45);
/// How often a stream checks that its host is still the one shown.
const SWITCH_CHECK: Duration = Duration::from_secs(1);
const PREVIEW_LOST: &str = "Commitarium's background service restarted, so this preview stopped. \
                            Start it again.";
const LOGIN_LOST: &str = "Commitarium's background service restarted during this login. \
                          Start the login again.";

/// How the main window reaches its host.
#[derive(Clone, Serialize)]
pub struct HostConnection {
    url: String,
    token: String,
    /// In remote mode, the coordinator's address through the bridge.
    coordinator_url: Option<String>,
    /// For app windows: the coordinator only (ADR-029). Never sent to a page
    /// with the rest of this.
    #[serde(skip)]
    coordinator_token: Option<String>,
    #[serde(skip)]
    bridge: Option<Bridge>,
}

/// How an app window reaches the coordinator in remote mode.
#[derive(Serialize)]
pub struct WindowConnection {
    coordinator_url: String,
    token: String,
}

struct Running {
    child: Child,
    // Holding this open is what keeps the host alive.
    _stdin: ChildStdin,
    connection: HostConnection,
}

/// This computer's host and the paired servers, side by side (ADR-040). The
/// window shows one of them, the active one, and switching only changes which.
/// Each starts the first time it is shown and keeps going until the app quits:
/// the local host's work stops then, a server's never depends on the app.
#[derive(Clone, Default)]
pub struct Host {
    running: Arc<Mutex<Option<Running>>>,
    /// Set once the host is stopped for good, so a page still polling cannot
    /// bring it back while the app exits or an update replaces its file.
    closing: Arc<AtomicBool>,
    /// The bridges to servers opened so far, by the server's fingerprint.
    servers: Arc<Mutex<BTreeMap<String, Result<HostConnection, String>>>>,
    /// The server the window shows, or `None` for this computer.
    active: Arc<Mutex<Option<String>>>,
}

impl Host {
    fn active(&self) -> Option<String> {
        self.active.lock().ok()?.clone()
    }

    /// The shown server's bridge, when a server is shown.
    fn remote(&self) -> Option<Result<HostConnection, String>> {
        let active = self.active()?;
        let servers = self.servers.lock().ok()?;
        Some(
            servers
                .get(&active)
                .cloned()
                .unwrap_or_else(|| Err("not connected to this server yet".to_string())),
        )
    }

    /// The shown host, starting this computer's again if it has gone.
    fn connection(&self, app: &AppHandle) -> Result<HostConnection, String> {
        if let Some(remote) = self.remote() {
            return remote;
        }
        self.local_connection(app)
    }

    fn local_connection(&self, app: &AppHandle) -> Result<HostConnection, String> {
        if self.closing.load(Ordering::Acquire) {
            return Err("the app is closing".to_string());
        }
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

    /// The shown host if it is running. Asking never starts one.
    fn running(&self) -> Option<HostConnection> {
        if let Some(remote) = self.remote() {
            return remote.ok();
        }
        self.local_running()
    }

    /// This computer's host if it is running, whichever is shown.
    fn local_running(&self) -> Option<HostConnection> {
        let mut running = self.running.lock().ok()?;
        let host = running.as_mut()?;
        matches!(host.child.try_wait(), Ok(None)).then(|| host.connection.clone())
    }

    /// Ends the host and waits for it, because Windows cannot replace a file
    /// that is still running.
    fn stop(&self) {
        self.closing.store(true, Ordering::Release);
        let Ok(mut running) = self.running.lock() else {
            return;
        };
        if let Some(mut host) = running.take() {
            let _ = host.child.kill();
            let _ = host.child.wait();
        }
    }

    /// Lets the host start again, after an update that was to replace it failed.
    pub(crate) fn reopen(&self) {
        self.closing.store(false, Ordering::Release);
    }
}

/// Starts what the app last showed: this computer's host, or the bridge to a
/// paired server. If it fails, the first call reports why, and a failed bridge
/// never falls back to the local host.
pub(crate) fn launch(app: &AppHandle) {
    if let Ok(Some(server)) = crate::remote::active_host(app) {
        tauri::async_runtime::block_on(show_server(app, &server));
        return;
    }
    if let Err(error) = app.state::<Host>().connection(app) {
        eprintln!("[host] {error}");
    }
}

/// Shows a server, opening its bridge the first time. A failed bridge stays
/// shown with its reason, and showing it again tries again.
pub(crate) async fn show_server(app: &AppHandle, server: &PairedHost) {
    let host = app.state::<Host>().inner().clone();
    let open = host
        .servers
        .lock()
        .map(|servers| matches!(servers.get(&server.fingerprint), Some(Ok(_))))
        .unwrap_or(false);
    if !open {
        let connected = connect_bridge(app, server).await;
        if let Ok(connection) = &connected {
            if let Some(bridge) = &connection.bridge {
                crate::app_pages::admit_server(app, bridge);
            }
        }
        if let Ok(mut servers) = host.servers.lock() {
            servers.insert(server.fingerprint.clone(), connected);
        }
    }
    if let Ok(mut active) = host.active.lock() {
        *active = Some(server.fingerprint.clone());
    };
}

/// Shows this computer. Its host starts when the window first asks for it.
pub(crate) fn show_this_computer(app: &AppHandle) {
    if let Ok(mut active) = app.state::<Host>().active.lock() {
        *active = None;
    }
}

/// Drops a server's bridge, so the next time it is shown it connects anew,
/// at whatever address it is saved at now.
pub(crate) fn drop_bridge(app: &AppHandle, fingerprint: &str) {
    if let Ok(mut servers) = app.state::<Host>().servers.lock() {
        servers.remove(fingerprint);
    }
}

/// Drops a forgotten server's bridge.
pub(crate) fn close_server(app: &AppHandle, fingerprint: &str) {
    let host = app.state::<Host>();
    drop_bridge(app, fingerprint);
    if host.active().as_deref() == Some(fingerprint) {
        show_this_computer(app);
    }
}

/// Every bridge open so far, for app windows' pages served after they opened.
pub(crate) fn open_bridges(app: &AppHandle) -> Vec<Bridge> {
    app.state::<Host>()
        .servers
        .lock()
        .map(|servers| {
            servers
                .values()
                .filter_map(|server| server.as_ref().ok()?.bridge.clone())
                .collect()
        })
        .unwrap_or_default()
}

async fn connect_bridge(app: &AppHandle, server: &PairedHost) -> Result<HostConnection, String> {
    let (token, coordinator_token) = (token()?, token()?);
    let bridge = crate::remote::open_bridge(
        server,
        token.clone(),
        coordinator_token.clone(),
        allowed_origins(app),
    )
    .await?;
    grant_main_window(app, &bridge.base_url())?;
    Ok(HostConnection {
        url: bridge.host_url(),
        token,
        coordinator_url: Some(bridge.coordinator_url()),
        coordinator_token: Some(coordinator_token),
        bridge: Some(bridge),
    })
}

/// Whether this computer's host is running: only then is there local work for
/// quitting or an update to wait for.
pub(crate) fn local_started(app: &AppHandle) -> bool {
    app.state::<Host>().local_running().is_some()
}

/// Whether the window shows a server rather than this computer.
pub(crate) fn on_server(app: &AppHandle) -> bool {
    app.state::<Host>().active().is_some()
}

/// Answers one call on a running host. There is no overall timeout, because
/// starting the stack is one long request, and no proxy, because this is
/// loopback. Bodies can hold secrets: nothing here logs them.
async fn request(
    connection: &HostConnection,
    method: Method,
    path: &str,
    body: Option<&Value>,
) -> Result<Value, String> {
    let client = reqwest::Client::builder()
        .no_proxy()
        .build()
        .map_err(|e| format!("reach the host: {e}"))?;
    let mut request = client
        .request(method, format!("{}{path}", connection.url))
        .bearer_auth(&connection.token);
    if let Some(body) = body {
        request = request.json(body);
    }
    let lost = |error: reqwest::Error| format!("could not reach the host: {}", error.without_url());
    let response = request.send().await.map_err(lost)?;
    let status = response.status();
    let answer = response.text().await.map_err(lost)?;
    let mut answer = read_answer(status, &answer)?;
    if let (Some(bridge), true) = (&connection.bridge, path.starts_with("/v1/previews")) {
        bridge.localize_previews(&mut answer).await;
    }
    Ok(answer)
}

/// The value of a success, and the work's own message on a failure.
fn read_answer(status: reqwest::StatusCode, answer: &str) -> Result<Value, String> {
    let value = serde_json::from_str::<Value>(answer);
    if status.is_success() {
        return value.map_err(|_| "the host gave an unreadable answer".to_string());
    }
    Err(value
        .ok()
        .and_then(|value| Some(value.get("error")?.as_str()?.to_string()))
        .unwrap_or_else(|| format!("the host answered {status}")))
}

async fn connected(app: &AppHandle) -> Result<HostConnection, String> {
    let host = app.state::<Host>().inner().clone();
    let app = app.clone();
    tauri::async_runtime::spawn_blocking(move || host.connection(&app))
        .await
        .map_err(|_| "could not reach the host".to_string())?
}

/// The coordinator of what the app shows, with the bearer token it takes:
/// through the bridge on a server, here on this computer without one.
pub(crate) async fn coordinator(app: &AppHandle) -> Result<(String, Option<String>), String> {
    let connection = connected(app).await?;
    Ok(match connection.coordinator_url {
        Some(url) => (url, connection.coordinator_token),
        None => ("http://127.0.0.1:8080".into(), None),
    })
}

/// A read on the host, starting it again if it has gone.
pub(crate) async fn get(app: &AppHandle, path: &str) -> Result<Value, String> {
    request(&connected(app).await?, Method::GET, path, None).await
}

/// Work on the host, starting it again if it has gone. A call the host dies
/// in the middle of fails rather than being repeated, because it may have
/// done part of its work.
pub(crate) async fn post(app: &AppHandle, path: &str, body: Value) -> Result<Value, String> {
    request(&connected(app).await?, Method::POST, path, Some(&body)).await
}

/// Waits for `work` from any thread, even one of a runtime's own, where
/// blocking directly would panic.
fn wait_for<T: Send>(work: impl Future<Output = T> + Send) -> T {
    thread::scope(|scope| {
        scope
            .spawn(|| tauri::async_runtime::block_on(work))
            .join()
            .unwrap_or_else(|panic| std::panic::resume_unwind(panic))
    })
}

/// How long leaving waits for the host to stop its previews and logins.
const LEAVING_TIMEOUT: Duration = Duration::from_secs(5);

/// Waits for `work` as wait_for does, but gives up after `limit`. Leaving
/// runs on the main thread, and a web view request finishing at that moment
/// waits for the main thread too, which can hold up the runtime the work
/// needs: without a limit the app froze on "Installing…" instead of
/// restarting. Work cut short carries on in the background until the app
/// exits.
fn wait_at_most(limit: Duration, work: impl Future<Output = ()> + Send + 'static) {
    let (done, finished) = mpsc::channel();
    thread::spawn(move || {
        tauri::async_runtime::block_on(work);
        let _ = done.send(());
    });
    let _ = finished.recv_timeout(limit);
}

pub(crate) fn post_blocking(app: &AppHandle, path: &str, body: Value) -> Result<Value, String> {
    wait_for(post(app, path, body))
}

async fn post_if_running(connection: &HostConnection, path: &str) -> Result<Value, String> {
    request(connection, Method::POST, path, Some(&Value::Null)).await
}

/// The app is leaving: previews stop, then logins, then the host. A host that
/// has already gone is not started again for this, and swept its leftovers
/// when it was last started, or will when the app next is.
pub(crate) fn wind_down(app: &AppHandle) {
    let host = app.state::<Host>().inner().clone();
    if let Some(connection) = host.local_running() {
        wait_at_most(LEAVING_TIMEOUT, async move {
            let _ = post_if_running(&connection, "/v1/previews/stop-all").await;
            let _ = post_if_running(&connection, "/v1/profiles/shutdown").await;
        });
    }
    host.stop();
}

/// The first half of quitting: logins, then the stack. Stopping the stack
/// needs nothing of the host's, so that still happens with the host gone (or
/// never started, when the app opened on a server), by a plain Docker call.
pub(crate) fn stop_logins_and_stack(app: &AppHandle) {
    let stopped = app
        .state::<Host>()
        .local_running()
        .is_some_and(|connection| {
            wait_for(async {
                let _ = post_if_running(&connection, "/v1/profiles/shutdown").await;
                post_if_running(&connection, "/v1/stack/down").await.is_ok()
            })
        });
    if !stopped && local_stack_up() {
        let _ = docker::stack_down_blocking();
    }
}

/// Whether any of this computer's Commitarium containers run, whichever place
/// is shown: a stack left up from an earlier session counts too. No Docker
/// means no stack.
pub(crate) fn local_stack_up() -> bool {
    docker::docker_command()
        .args([
            "ps",
            "--quiet",
            "--filter",
            &format!("label=com.docker.compose.project={}", docker::PROJECT_NAME),
        ])
        .output()
        .is_ok_and(|out| out.status.success() && !out.stdout.trim_ascii().is_empty())
}

/// What agents are doing on this computer's coordinator right now. A
/// coordinator that does not answer has nothing worth asking about.
pub(crate) fn local_running_work() -> Vec<Value> {
    let (sent, received) = mpsc::channel();
    thread::spawn(move || {
        let work = tauri::async_runtime::block_on(async {
            let client = reqwest::Client::builder()
                .no_proxy()
                .timeout(Duration::from_secs(3))
                .build()
                .ok()?;
            let answer: Value = client
                .get("http://127.0.0.1:8080/api/v1/attention")
                .send()
                .await
                .ok()?
                .error_for_status()
                .ok()?
                .json()
                .await
                .ok()?;
            answer.get("running")?.as_array().cloned()
        });
        let _ = sent.send(work.unwrap_or_default());
    });
    received
        .recv_timeout(Duration::from_secs(4))
        .unwrap_or_default()
}

/// Whether the window shows a server rather than this computer.
pub(crate) fn server_shown(app: &AppHandle) -> bool {
    app.state::<Host>().remote().is_some()
}

/// Windows cannot replace a running file, so the host goes before the
/// installer runs. Previews are left to the next start's sweep, as they have
/// always been on this path.
pub(crate) fn stop_for_update(app: &AppHandle) {
    let host = app.state::<Host>().inner().clone();
    if let Some(connection) = host.local_running() {
        wait_at_most(LEAVING_TIMEOUT, async move {
            let _ = post_if_running(&connection, "/v1/profiles/shutdown").await;
        });
    }
    host.stop();
}

/// What was under way in a host, as its updates showed. Its previews and
/// logins live only in that process, so a new host knows nothing of them, and
/// their windows would otherwise wait for an update that never comes.
#[derive(Default)]
struct LiveWork {
    previews: BTreeMap<String, Value>,
    logins: BTreeSet<String>,
}

impl LiveWork {
    fn note(&mut self, name: &str, payload: &Value) {
        if name == preview::STATUS_EVENT {
            let key = format!("{}{}", payload["projectId"], payload["target"]);
            match payload["state"].as_str() {
                Some("starting" | "running") => {
                    self.previews.insert(key, payload.clone());
                }
                _ => {
                    self.previews.remove(&key);
                }
            }
        } else if name == profiles::PROGRESS_EVENT {
            let Some(profile) = payload["profileId"].as_str() else {
                return;
            };
            match payload["status"].as_str() {
                Some(
                    "starting"
                    | "waiting_for_browser"
                    | "waiting_for_code"
                    | "waiting_for_api_key"
                    | "verifying",
                ) => {
                    self.logins.insert(profile.to_string());
                }
                _ => {
                    self.logins.remove(profile);
                }
            }
        }
    }

    /// The last status of each preview, now as a preview that has failed.
    fn lost_previews(&mut self) -> Vec<Value> {
        let mut previews = std::mem::take(&mut self.previews);
        previews
            .values_mut()
            .map(|status| {
                status["state"] = json!("failed");
                status["urls"] = json!([]);
                status["error"] = json!(PREVIEW_LOST);
                status.take()
            })
            .collect()
    }

    /// What to tell each login's window, from the profiles of the new host.
    fn lost_logins(&mut self, profiles: Option<&Value>) -> Vec<Value> {
        let status_of = |id: &str| {
            let profile = profiles?.as_array()?.iter().find(|p| p["id"] == id)?;
            Some(profile["status"].clone())
        };
        std::mem::take(&mut self.logins)
            .into_iter()
            .map(|id| {
                let status = status_of(&id).unwrap_or_else(|| json!("failed"));
                json!({
                    "profileId": id,
                    "status": status,
                    "detail": { "message": LOGIN_LOST },
                })
            })
            .collect()
    }
}

/// Repeats the host's updates as Tauri events, so pages and app windows keep
/// listening the way they always have. A host that has died is started again
/// here, because jobs and work run there with no page asking for them. One
/// stopped for an update stays down: `connected` refuses while it is closing.
pub(crate) fn relay_events(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        let client = reqwest::Client::builder().no_proxy().build();
        let Ok(client) = client else {
            return;
        };
        let mut live = LiveWork::default();
        // The shown host's address and token: a new token at the same address
        // is that host started again, a new address is a switch.
        let mut followed: Option<(String, String)> = None;
        loop {
            let running = app.state::<Host>().running();
            let connection = match (running, &followed) {
                (None, Some(_)) => connected(&app).await.ok(),
                (running, _) => running,
            };
            if let Some(connection) = connection {
                match &followed {
                    Some((url, token)) if *url == connection.url && *token != connection.token => {
                        report_lost_work(&app, &connection, &mut live).await;
                    }
                    // The other side's work is not lost, only not shown.
                    Some((url, _)) if *url != connection.url => live = LiveWork::default(),
                    _ => {}
                }
                followed = Some((connection.url.clone(), connection.token.clone()));
                let _ = relay(&app, &client, &connection, &mut live).await;
            }
            tokio::time::sleep(RELISTEN_AFTER).await;
        }
    });
}

async fn report_lost_work(app: &AppHandle, connection: &HostConnection, live: &mut LiveWork) {
    for status in live.lost_previews() {
        let _ = app.emit(preview::STATUS_EVENT, status);
    }
    if live.logins.is_empty() {
        return;
    }
    let profiles = request(connection, Method::GET, "/v1/profiles", None).await;
    for progress in live.lost_logins(profiles.ok().as_ref()) {
        let _ = app.emit(profiles::PROGRESS_EVENT, progress);
    }
}

async fn relay(
    app: &AppHandle,
    client: &reqwest::Client,
    connection: &HostConnection,
    live: &mut LiveWork,
) -> Result<(), reqwest::Error> {
    let mut stream = client
        .get(format!("{}/v1/events", connection.url))
        .bearer_auth(&connection.token)
        .send()
        .await?
        .error_for_status()?;
    if connection.bridge.is_some() {
        catch_up_on_logins(app, connection, live).await;
    }
    let host = app.state::<Host>().inner().clone();
    let shown = host.active();
    let mut frames = Frames::default();
    let mut quiet = Duration::ZERO;
    // Checked each second, so a switch moves the stream to the new host at once.
    while host.active() == shown {
        let Ok(piece) = tokio::time::timeout(SWITCH_CHECK, stream.chunk()).await else {
            quiet += SWITCH_CHECK;
            if quiet >= SILENCE_LIMIT {
                break;
            }
            continue;
        };
        quiet = Duration::ZERO;
        let Some(piece) = piece? else {
            break;
        };
        for (name, data) in frames.push(&piece) {
            if let Ok(mut payload) = serde_json::from_str::<Value>(&data) {
                if let (Some(bridge), preview::STATUS_EVENT) = (&connection.bridge, name.as_str()) {
                    bridge.localize_previews(&mut payload).await;
                }
                live.note(&name, &payload);
                let _ = app.emit(&name, payload);
            }
        }
    }
    Ok(())
}

/// A server's updates can be lost while its connection is down, and a window
/// would wait for them, so each new connection starts from the server's state.
async fn catch_up_on_logins(app: &AppHandle, connection: &HostConnection, live: &mut LiveWork) {
    let Ok(profiles) = request(connection, Method::GET, "/v1/profiles", None).await else {
        return;
    };
    for progress in login_progress(&profiles) {
        live.note(profiles::PROGRESS_EVENT, &progress);
        let _ = app.emit(profiles::PROGRESS_EVENT, progress);
    }
}

fn login_progress(profiles: &Value) -> Vec<Value> {
    let profiles = profiles.as_array().map(Vec::as_slice).unwrap_or_default();
    profiles
        .iter()
        .map(|profile| {
            json!({
                "profileId": profile["id"],
                "status": profile["status"],
                "detail": profile["detail"],
            })
        })
        .collect()
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
            coordinator_url: None,
            coordinator_token: None,
            bridge: None,
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

/// `None` on this computer, where an app window finds the coordinator itself.
#[tauri::command]
pub fn coordinator_connection(app: AppHandle) -> Option<WindowConnection> {
    let connection = app.state::<Host>().remote().and_then(Result::ok)?;
    Some(WindowConnection {
        coordinator_url: connection.coordinator_url?,
        token: connection.coordinator_token?,
    })
}

/// The local address of the shown server's Forgejo, for the browser
/// (SERVER_COMPLETE_PLAN.md, S4); `None` on this computer, where Forgejo is
/// on its own port.
#[tauri::command]
pub async fn forgejo_address(app: AppHandle) -> Result<Option<String>, String> {
    let Some(bridge) = app
        .state::<Host>()
        .remote()
        .and_then(Result::ok)
        .and_then(|c| c.bridge)
    else {
        return Ok(None);
    };
    bridge.forgejo_url().await.map(Some)
}

#[tauri::command]
pub async fn host_connection(app: AppHandle) -> Result<HostConnection, String> {
    connected(&app).await
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn leaving_does_not_wait_for_work_that_never_finishes() {
        let started = std::time::Instant::now();
        wait_at_most(Duration::from_millis(200), std::future::pending());
        assert!(started.elapsed() < Duration::from_secs(2));
    }

    /// One fake host that answers every request the same way.
    fn host_answering(status: u16, body: &'static str) -> HostConnection {
        let server = tiny_http::Server::http("127.0.0.1:0").unwrap();
        let port = server.server_addr().to_ip().unwrap().port();
        thread::spawn(move || {
            for request in server.incoming_requests() {
                let response = tiny_http::Response::from_string(body).with_status_code(status);
                let _ = request.respond(response);
            }
        });
        HostConnection {
            url: format!("http://127.0.0.1:{port}"),
            token: "token".into(),
            coordinator_url: None,
            coordinator_token: None,
            bridge: None,
        }
    }

    #[test]
    fn a_new_connection_shows_a_login_as_it_stands_on_the_server() {
        let profiles = json!([{
            "id": "codex",
            "name": "Codex",
            "provider": "codex",
            "status": "waiting_for_code",
            "detail": { "deviceCode": "ABCD-12345" },
        }]);
        assert_eq!(
            login_progress(&profiles),
            vec![json!({
                "profileId": "codex",
                "status": "waiting_for_code",
                "detail": { "deviceCode": "ABCD-12345" },
            })]
        );
    }

    #[test]
    fn a_forwarder_returns_the_hosts_json_untouched() {
        let host = host_answering(200, r#"{"state":"running","urls":[]}"#);
        let answer = tauri::async_runtime::block_on(request(
            &host,
            Method::POST,
            "/v1/previews/status",
            Some(&json!({ "project_id": "p" })),
        ));
        assert_eq!(answer, Ok(json!({ "state": "running", "urls": [] })));
    }

    #[test]
    fn a_forwarder_keeps_the_works_exact_error() {
        let host = host_answering(500, r#"{"error":"a login is already in progress"}"#);
        let answer = tauri::async_runtime::block_on(post_if_running(&host, "/v1/profiles/verify"));
        assert_eq!(answer, Err("a login is already in progress".to_string()));
    }

    #[test]
    fn a_forwarder_explains_an_answer_it_cannot_read() {
        let host = host_answering(502, "not json");
        let answer = tauri::async_runtime::block_on(post_if_running(&host, "/v1/stack/up"));
        assert!(answer.unwrap_err().contains("502"));
    }

    #[test]
    fn a_host_gone_mid_call_is_reported_not_repeated() {
        let host = HostConnection {
            url: "http://127.0.0.1:9".into(),
            token: "token".into(),
            coordinator_url: None,
            coordinator_token: None,
            bridge: None,
        };
        let answer = tauri::async_runtime::block_on(post_if_running(&host, "/v1/stack/up"));
        assert!(answer.unwrap_err().starts_with("could not reach the host"));
    }

    #[test]
    fn work_can_be_waited_for_from_inside_a_runtime() {
        let answer = tauri::async_runtime::block_on(async { wait_for(async { 1 }) });
        assert_eq!(answer, 1);
    }

    #[test]
    fn previews_cut_off_by_a_new_host_are_reported_as_failed() {
        let mut live = LiveWork::default();
        let status = |project: &str, state: &str| {
            json!({
                "projectId": project,
                "target": { "kind": "canonical" },
                "commitId": "abc",
                "state": state,
                "urls": [{ "service": "web", "url": "http://127.0.0.1:1", "open": true }],
                "error": null,
            })
        };
        live.note(preview::STATUS_EVENT, &status("gone", "running"));
        live.note(preview::STATUS_EVENT, &status("ended", "running"));
        live.note(preview::STATUS_EVENT, &status("ended", "stopped"));

        let lost = live.lost_previews();
        assert_eq!(lost.len(), 1);
        assert_eq!(lost[0]["projectId"], "gone");
        assert_eq!(lost[0]["commitId"], "abc");
        assert_eq!(lost[0]["state"], "failed");
        assert_eq!(lost[0]["urls"], json!([]));
        assert_eq!(lost[0]["error"], PREVIEW_LOST);
        assert!(live.lost_previews().is_empty());
    }

    #[test]
    fn logins_cut_off_by_a_new_host_take_the_profiles_real_status() {
        let mut live = LiveWork::default();
        let progress = |id: &str, status: &str| json!({ "profileId": id, "status": status });
        live.note(
            profiles::PROGRESS_EVENT,
            &progress("codex", "waiting_for_browser"),
        );
        live.note(
            profiles::PROGRESS_EVENT,
            &progress("claude", "waiting_for_code"),
        );
        live.note(profiles::PROGRESS_EVENT, &progress("done", "starting"));
        live.note(profiles::PROGRESS_EVENT, &progress("done", "connected"));

        let profiles = json!([{ "id": "codex", "status": "connected" }]);
        let lost = live.lost_logins(Some(&profiles));
        assert_eq!(lost.len(), 2);
        assert_eq!(lost[0]["profileId"], "claude");
        assert_eq!(lost[0]["status"], "failed");
        assert_eq!(lost[1]["profileId"], "codex");
        assert_eq!(lost[1]["status"], "connected");
        assert_eq!(lost[1]["detail"]["message"], LOGIN_LOST);
    }

    /// Only one process may hold this state, or a login or preview started in
    /// one would be invisible to the other.
    #[test]
    fn the_desktop_never_makes_the_hosts_state() {
        let mut found = Vec::new();
        let manifest = std::path::Path::new(env!("CARGO_MANIFEST_DIR"));
        let mut directories = vec![manifest.join("src")];
        while let Some(directory) = directories.pop() {
            for entry in std::fs::read_dir(directory).unwrap() {
                let path = entry.unwrap().path();
                if path.is_dir() {
                    directories.push(path);
                } else {
                    found.push(path);
                }
            }
        }
        for path in found.iter().filter(|path| !path.ends_with("host.rs")) {
            let text = std::fs::read_to_string(path).unwrap();
            for state in ["ProfileManager", "PreviewManager", "CliUpdater"] {
                assert!(!text.contains(state), "{} uses {state}", path.display());
            }
        }
    }

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
