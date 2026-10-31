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
const PREVIEW_LOST: &str = "Commitarium's background service restarted, so this preview stopped. \
                            Start it again.";
const LOGIN_LOST: &str = "Commitarium's background service restarted during this login. \
                          Start the login again.";

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
    /// Set once the host is stopped for good, so a page still polling cannot
    /// bring it back while the app exits or an update replaces its file.
    closing: Arc<AtomicBool>,
}

impl Host {
    /// The running host, starting it again if it has gone.
    fn connection(&self, app: &AppHandle) -> Result<HostConnection, String> {
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

    /// The host if it is running. Listening never starts one, or it would
    /// bring back a host stopped for an update.
    fn running(&self) -> Option<HostConnection> {
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

/// Starts the host at launch. If it fails, the first call tries again and
/// reports why.
pub(crate) fn launch(app: &AppHandle) {
    if let Err(error) = app.state::<Host>().connection(app) {
        eprintln!("[host] {error}");
    }
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
    read_answer(status, &answer)
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
    if let Some(connection) = host.running() {
        wait_for(async {
            let _ = post_if_running(&connection, "/v1/previews/stop-all").await;
            let _ = post_if_running(&connection, "/v1/profiles/shutdown").await;
        });
    }
    host.stop();
}

/// The first half of quitting with "stop the stack": logins, then the stack.
/// Stopping the stack needs nothing of the host's, so that still happens with
/// the host gone, by a plain Docker call.
pub(crate) fn stop_logins_and_stack(app: &AppHandle) {
    let stopped = app.state::<Host>().running().is_some_and(|connection| {
        wait_for(async {
            let _ = post_if_running(&connection, "/v1/profiles/shutdown").await;
            post_if_running(&connection, "/v1/stack/down").await.is_ok()
        })
    });
    if !stopped {
        let _ = docker::stack_down_blocking();
    }
}

/// Windows cannot replace a running file, so the host goes before the
/// installer runs. Previews are left to the next start's sweep, as they have
/// always been on this path.
pub(crate) fn stop_for_update(app: &AppHandle) {
    let host = app.state::<Host>().inner().clone();
    if let Some(connection) = host.running() {
        let _ = wait_for(post_if_running(&connection, "/v1/profiles/shutdown"));
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
/// listening the way they always have. It only ever follows a host that is
/// already running, and picks up a new one when a call has started it.
pub(crate) fn relay_events(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        let client = reqwest::Client::builder().no_proxy().build();
        let Ok(client) = client else {
            return;
        };
        let mut live = LiveWork::default();
        let mut followed: Option<String> = None;
        loop {
            if let Some(connection) = app.state::<Host>().running() {
                if followed.is_some_and(|token| token != connection.token) {
                    report_lost_work(&app, &connection, &mut live).await;
                }
                followed = Some(connection.token.clone());
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
    let mut frames = Frames::default();
    while let Some(piece) = stream.chunk().await? {
        for (name, data) in frames.push(&piece) {
            if let Ok(payload) = serde_json::from_str::<Value>(&data) {
                live.note(&name, &payload);
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
pub async fn host_connection(app: AppHandle) -> Result<HostConnection, String> {
    connected(&app).await
}

#[cfg(test)]
mod tests {
    use super::*;

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
        }
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
