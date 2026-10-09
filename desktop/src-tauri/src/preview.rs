//! Disposable project previews backed by a validated Docker Compose model.
//!
//! The renderer supplies only a project identity and target. This module reads
//! the trusted coordinator contract, fetches the canonical commit, asks
//! `preview_compose` to normalize and rewrite the repository Compose file, and
//! only launches that rewritten file.

use crate::{docker, preview_compose};
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::collections::{HashMap, HashSet, VecDeque};
use std::fs;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::{mpsc, Arc, Mutex};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Emitter, State};

const STATUS_EVENT: &str = "preview-status-changed";
const PREVIEW_TEMP_PREFIX: &str = "commitarium-preview-";
const COMPOSE_PROJECT_LABEL: &str = "com.docker.compose.project";
const READY_TIMEOUT: Duration = Duration::from_secs(10 * 60);
const LOG_LIMIT: usize = 400;

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum PreviewTarget {
    Canonical,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "lowercase")]
pub enum PreviewState {
    Starting,
    Running,
    Failed,
    Stopped,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PreviewUrl {
    service: String,
    url: String,
    open: bool,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct PreviewStatus {
    project_id: String,
    target: PreviewTarget,
    commit_id: String,
    state: PreviewState,
    urls: Vec<PreviewUrl>,
    error: Option<String>,
}

#[derive(Clone, Debug, Deserialize)]
struct RunConfig {
    open: RunOpen,
}

#[derive(Clone, Debug, Deserialize)]
struct RunOpen {
    service: String,
    port: u16,
}

#[derive(Deserialize)]
struct ToolchainManifest {
    status: String,
    run: Option<RunConfig>,
}

#[derive(Deserialize)]
struct ProjectHandoff {
    project_id: String,
    source: ProjectHandoffSource,
}

#[derive(Deserialize)]
struct ProjectHandoffSource {
    repository: Repository,
    default_branch: String,
    head_commit_id: String,
}

#[derive(Deserialize)]
struct Repository {
    owner: String,
    name: String,
}

#[derive(Clone)]
struct ComposeRuntime {
    project_name: String,
    compose_file: PathBuf,
    environment_file: PathBuf,
    source_root: PathBuf,
}

struct PreviewRecord {
    status: PreviewStatus,
    generation: u64,
    runtime: Option<ComposeRuntime>,
    logs: VecDeque<String>,
}

struct StoppedPreview {
    status: PreviewStatus,
    runtime: Option<ComposeRuntime>,
}

struct PreviewLogSource {
    runtime: Option<ComposeRuntime>,
    cached: Vec<String>,
}

#[derive(Clone, Default)]
pub struct PreviewManager {
    inner: Arc<Mutex<PreviewRegistry>>,
}

#[derive(Default)]
struct PreviewRegistry {
    generation: u64,
    projects: HashMap<String, PreviewRecord>,
}

impl PreviewManager {
    fn begin(&self, status: PreviewStatus) -> Result<u64, String> {
        let mut registry = self
            .inner
            .lock()
            .map_err(|_| "preview state is unavailable")?;
        registry.generation += 1;
        let generation = registry.generation;
        registry.projects.insert(
            status.project_id.clone(),
            PreviewRecord {
                status,
                generation,
                runtime: None,
                logs: VecDeque::new(),
            },
        );
        Ok(generation)
    }

    fn status(&self, project_id: &str) -> Result<Option<PreviewStatus>, String> {
        let registry = self
            .inner
            .lock()
            .map_err(|_| "preview state is unavailable")?;
        Ok(registry
            .projects
            .get(project_id)
            .map(|record| record.status.clone()))
    }

    fn attach(&self, project_id: &str, generation: u64, runtime: ComposeRuntime) -> bool {
        let Ok(mut registry) = self.inner.lock() else {
            return false;
        };
        let Some(record) = registry.projects.get_mut(project_id) else {
            return false;
        };
        if record.generation != generation || record.status.state != PreviewState::Starting {
            return false;
        }
        record.runtime = Some(runtime);
        true
    }

    fn append_log(&self, project_id: &str, generation: u64, line: String) {
        let Ok(mut registry) = self.inner.lock() else {
            return;
        };
        let Some(record) = registry.projects.get_mut(project_id) else {
            return;
        };
        if record.generation != generation {
            return;
        }
        if record.logs.len() == LOG_LIMIT {
            record.logs.pop_front();
        }
        record.logs.push_back(line);
    }

    fn active(&self, project_id: &str, generation: u64, state: PreviewState) -> bool {
        self.inner.lock().ok().is_some_and(|registry| {
            registry.projects.get(project_id).is_some_and(|record| {
                record.generation == generation && record.status.state == state
            })
        })
    }

    fn running(
        &self,
        project_id: &str,
        generation: u64,
        urls: Vec<PreviewUrl>,
    ) -> Option<PreviewStatus> {
        let mut registry = self.inner.lock().ok()?;
        let record = registry.projects.get_mut(project_id)?;
        if record.generation != generation || record.status.state != PreviewState::Starting {
            return None;
        }
        record.status.state = PreviewState::Running;
        record.status.urls = urls;
        Some(record.status.clone())
    }

    fn fail(
        &self,
        project_id: &str,
        generation: u64,
        fallback: &str,
    ) -> Option<(PreviewStatus, Option<ComposeRuntime>)> {
        let mut registry = self.inner.lock().ok()?;
        let record = registry.projects.get_mut(project_id)?;
        if record.generation != generation || record.status.state == PreviewState::Stopped {
            return None;
        }
        let detail = if record.logs.is_empty() {
            fallback.to_string()
        } else {
            let recent = record
                .logs
                .iter()
                .rev()
                .take(20)
                .cloned()
                .collect::<Vec<_>>()
                .into_iter()
                .rev()
                .collect::<Vec<_>>()
                .join("\n");
            if recent.contains(fallback) {
                recent
            } else {
                format!("{fallback}\n\n{recent}")
            }
        };
        record.status.state = PreviewState::Failed;
        record.status.error = Some(detail);
        record.status.urls.clear();
        Some((record.status.clone(), record.runtime.take()))
    }

    fn stop(&self, project_id: &str) -> Result<Option<StoppedPreview>, String> {
        let mut registry = self
            .inner
            .lock()
            .map_err(|_| "preview state is unavailable")?;
        let Some(record) = registry.projects.get_mut(project_id) else {
            return Ok(None);
        };
        record.status.state = PreviewState::Stopped;
        record.status.urls.clear();
        record.status.error = None;
        Ok(Some(StoppedPreview {
            status: record.status.clone(),
            runtime: record.runtime.take(),
        }))
    }

    fn logs(&self, project_id: &str) -> Result<Option<PreviewLogSource>, String> {
        let registry = self
            .inner
            .lock()
            .map_err(|_| "preview state is unavailable")?;
        Ok(registry
            .projects
            .get(project_id)
            .map(|record| PreviewLogSource {
                runtime: record.runtime.clone(),
                cached: record.logs.iter().cloned().collect(),
            }))
    }
}

#[tauri::command]
pub async fn start_preview(
    app: AppHandle,
    manager: State<'_, PreviewManager>,
    project_id: String,
    target: PreviewTarget,
) -> Result<PreviewStatus, String> {
    validate_identifier("project ID", &project_id)?;
    let client = reqwest::Client::new();
    let manifest: ToolchainManifest = coordinator_json(
        client
            .get(coordinator_url(&format!(
                "/api/v1/projects/{project_id}/toolchain"
            )))
            .send()
            .await
            .map_err(|e| format!("request project stack from coordinator: {e}"))?,
        "project stack",
    )
    .await?;
    if manifest.status != "configured" {
        return Err("choose a project stack before starting a preview".into());
    }
    let run = manifest
        .run
        .ok_or("this project's stack has no preview open target")?;
    validate_run(&run)?;
    let handoff: ProjectHandoff = coordinator_json(
        client
            .get(coordinator_url(&format!(
                "/api/v1/projects/{project_id}/handoff"
            )))
            .send()
            .await
            .map_err(|e| format!("request project handoff from coordinator: {e}"))?,
        "project handoff",
    )
    .await?;
    validate_handoff(&handoff, &project_id)?;

    let owned = manager.inner().clone();
    stop_project(&app, owned.clone(), &project_id).await?;
    let status = PreviewStatus {
        project_id: project_id.clone(),
        target,
        commit_id: handoff.source.head_commit_id.clone(),
        state: PreviewState::Starting,
        urls: Vec::new(),
        error: None,
    };
    let generation = owned.begin(status.clone())?;
    let _ = app.emit(STATUS_EVENT, status.clone());
    let task_app = app.clone();
    tauri::async_runtime::spawn(async move {
        let launch_app = task_app.clone();
        let launch_manager = owned.clone();
        let launch_project = project_id.clone();
        let result = tauri::async_runtime::spawn_blocking(move || {
            launch_preview(
                launch_app,
                launch_manager,
                launch_project,
                generation,
                handoff,
                run,
            )
        })
        .await;
        if result.is_err() {
            fail_preview(
                &task_app,
                &owned,
                &project_id,
                generation,
                "preview task stopped unexpectedly",
                None,
            );
        }
    });
    Ok(status)
}

#[tauri::command]
pub async fn stop_preview(
    app: AppHandle,
    manager: State<'_, PreviewManager>,
    project_id: String,
) -> Result<(), String> {
    validate_identifier("project ID", &project_id)?;
    stop_project(&app, manager.inner().clone(), &project_id).await
}

#[tauri::command]
pub async fn reset_preview_data(
    app: AppHandle,
    manager: State<'_, PreviewManager>,
    project_id: String,
) -> Result<(), String> {
    cleanup_preview_data(&app, manager.inner().clone(), &project_id, false).await
}

pub(crate) async fn delete_project_data(
    app: &AppHandle,
    manager: PreviewManager,
    project_id: &str,
) -> Result<(), String> {
    cleanup_preview_data(app, manager, project_id, true).await
}

#[tauri::command]
pub fn get_preview_status(
    manager: State<'_, PreviewManager>,
    project_id: String,
) -> Result<Option<PreviewStatus>, String> {
    validate_identifier("project ID", &project_id)?;
    manager.status(&project_id)
}

#[tauri::command]
pub async fn get_preview_logs(
    manager: State<'_, PreviewManager>,
    project_id: String,
) -> Result<Vec<String>, String> {
    validate_identifier("project ID", &project_id)?;
    let Some(source) = manager.logs(&project_id)? else {
        return Ok(Vec::new());
    };
    let Some(runtime) = source.runtime else {
        return Ok(source.cached);
    };
    let logs = tauri::async_runtime::spawn_blocking(move || compose_logs(&runtime))
        .await
        .map_err(|_| "preview log task stopped unexpectedly".to_string())?;
    let mut combined = source.cached;
    if let Ok(lines) = logs {
        combined.extend(lines);
    }
    if combined.len() > LOG_LIMIT {
        combined.drain(..combined.len() - LOG_LIMIT);
    }
    Ok(combined)
}

async fn stop_project(
    app: &AppHandle,
    manager: PreviewManager,
    project_id: &str,
) -> Result<(), String> {
    let Some(stopped) = manager.stop(project_id)? else {
        return Ok(());
    };
    let _ = app.emit(STATUS_EVENT, stopped.status);
    tauri::async_runtime::spawn_blocking(move || {
        if let Some(runtime) = stopped.runtime {
            let _ = compose_down(&runtime, false, false);
            remove_source(&runtime.source_root);
        }
    })
    .await
    .map_err(|_| "preview stop task stopped unexpectedly".to_string())?;
    Ok(())
}

async fn cleanup_preview_data(
    app: &AppHandle,
    manager: PreviewManager,
    project_id: &str,
    images: bool,
) -> Result<(), String> {
    validate_identifier("project ID", project_id)?;
    let stopped = manager.stop(project_id)?;
    if let Some(stopped) = &stopped {
        let _ = app.emit(STATUS_EVENT, stopped.status.clone());
    }
    let compose_project = preview_compose::project_name(project_id)?;
    tauri::async_runtime::spawn_blocking(move || {
        let mut down_error = None;
        if let Some(runtime) = stopped.and_then(|stopped| stopped.runtime) {
            if let Err(error) = compose_down(&runtime, true, images) {
                down_error = Some(error);
            }
            remove_source(&runtime.source_root);
        }
        remove_labeled_resources("container", &compose_project);
        remove_labeled_resources("network", &compose_project);
        remove_labeled_resources("volume", &compose_project);
        if images {
            remove_labeled_resources("image", &compose_project);
        }
        if let Some(error) = down_error {
            Err(error)
        } else {
            Ok(())
        }
    })
    .await
    .map_err(|_| "preview data cleanup task stopped unexpectedly".to_string())?
}

fn launch_preview(
    app: AppHandle,
    manager: PreviewManager,
    project_id: String,
    generation: u64,
    handoff: ProjectHandoff,
    run: RunConfig,
) {
    let root = match prepare_source(&handoff) {
        Ok(path) => path,
        Err(error) => {
            fail_preview(&app, &manager, &project_id, generation, &error, None);
            return;
        }
    };
    let source = root.join("source");
    let open = preview_compose::OpenTarget {
        service: run.open.service.clone(),
        port: run.open.port,
    };
    let prepared = match preview_compose::normalize_and_rewrite(&source, &project_id, &open) {
        Ok(prepared) => prepared,
        Err(error) => {
            fail_preview(&app, &manager, &project_id, generation, &error, None);
            remove_source(&root);
            return;
        }
    };
    let compose_file = root.join("preview.compose.json");
    if let Err(error) = preview_compose::write_rewritten(&prepared, &compose_file) {
        fail_preview(&app, &manager, &project_id, generation, &error, None);
        remove_source(&root);
        return;
    }
    let environment_file = root.join("preview.env");
    if let Err(error) = fs::write(&environment_file, []) {
        fail_preview(
            &app,
            &manager,
            &project_id,
            generation,
            &format!("write isolated preview environment file: {error}"),
            None,
        );
        remove_source(&root);
        return;
    }
    let runtime = ComposeRuntime {
        project_name: prepared.project_name.clone(),
        compose_file,
        environment_file,
        source_root: root,
    };
    if !manager.attach(&project_id, generation, runtime.clone()) {
        remove_source(&runtime.source_root);
        return;
    }
    manager.append_log(
        &project_id,
        generation,
        "[preview] Building and starting the Compose stack.".into(),
    );
    match compose_stream_up(&runtime, &manager, &project_id, generation) {
        Ok(()) => {}
        Err(error) => {
            manager.append_log(&project_id, generation, format!("[preview] {error}"));
            fail_preview(&app, &manager, &project_id, generation, &error, None);
            return;
        }
    }
    if !manager.active(&project_id, generation, PreviewState::Starting) {
        let _ = compose_down(&runtime, false, false);
        remove_source(&runtime.source_root);
        return;
    }

    let urls = match resolve_urls(&runtime, &prepared.published_ports, &run.open) {
        Ok(urls) => urls,
        Err(error) => {
            fail_preview(&app, &manager, &project_id, generation, &error, None);
            return;
        }
    };
    let Some(open_url) = urls.iter().find(|url| url.open) else {
        fail_preview(
            &app,
            &manager,
            &project_id,
            generation,
            "the preview open target has no URL",
            None,
        );
        return;
    };
    if let Err(error) = wait_for_http(
        &manager,
        &project_id,
        generation,
        &runtime,
        &run.open.service,
        &open_url.url,
    ) {
        fail_preview(&app, &manager, &project_id, generation, &error, None);
        return;
    }
    if let Some(status) = manager.running(&project_id, generation, urls) {
        let _ = app.emit(STATUS_EVENT, status);
        watch_stack(
            app,
            manager,
            project_id,
            generation,
            runtime,
            run.open.service,
        );
    } else {
        let _ = compose_down(&runtime, false, false);
        remove_source(&runtime.source_root);
    }
}

fn prepare_source(handoff: &ProjectHandoff) -> Result<PathBuf, String> {
    let temporary = tempfile::Builder::new()
        .prefix(PREVIEW_TEMP_PREFIX)
        .tempdir()
        .map_err(|e| format!("create preview source directory: {e}"))?;
    let root = temporary.path().to_path_buf();
    let source = root.join("source");
    fs::create_dir_all(&source).map_err(|e| format!("create preview checkout: {e}"))?;
    run_checked(
        Command::new("git").arg("init").arg(&source),
        "initialize preview checkout",
    )?;
    let reference = format!("refs/heads/{}", handoff.source.default_branch);
    run_checked(
        Command::new("git").args(["check-ref-format", &reference]),
        "validate preview branch",
    )?;
    let repository = internal_repository_url(&handoff.source.repository)?;
    let token_path = forgejo_token_path()?;
    let token =
        fs::read_to_string(token_path).map_err(|_| "read internal Forgejo token".to_string())?;
    let token = token.trim();
    if token.is_empty() || token.chars().any(char::is_whitespace) {
        return Err("internal Forgejo token is invalid".into());
    }
    let refspec = format!("+{reference}:refs/heads/commitarium-preview");
    let mut fetch = Command::new("git");
    fetch
        .current_dir(&source)
        .args([
            "fetch",
            "--depth=1",
            "--no-tags",
            "--force",
            "--",
            &repository,
            &refspec,
        ])
        .env("GIT_TERMINAL_PROMPT", "0")
        .env("GIT_CONFIG_COUNT", "2")
        .env("GIT_CONFIG_KEY_0", "http.extraHeader")
        .env(
            "GIT_CONFIG_VALUE_0",
            format!("Authorization: token {token}"),
        )
        .env("GIT_CONFIG_KEY_1", "credential.helper")
        .env("GIT_CONFIG_VALUE_1", "");
    run_checked(&mut fetch, "fetch canonical preview source")?;
    let fetched = command_line(
        Command::new("git")
            .current_dir(&source)
            .args(["rev-parse", "refs/heads/commitarium-preview"]),
        "inspect preview commit",
    )?;
    if fetched != handoff.source.head_commit_id {
        return Err("the canonical branch advanced while the preview was starting; retry".into());
    }
    run_checked(
        Command::new("git")
            .current_dir(&source)
            .args(["checkout", "--detach", &fetched]),
        "check out preview commit",
    )?;
    Ok(temporary.keep())
}

fn compose_command(runtime: &ComposeRuntime) -> Command {
    let mut command = docker::docker_command();
    preview_compose::sanitize_compose_command(&mut command, &runtime.source_root);
    command
        .arg("compose")
        .arg("--env-file")
        .arg(&runtime.environment_file)
        .args(["-p", &runtime.project_name, "-f"])
        .arg(&runtime.compose_file);
    command
}

fn compose_capture(
    runtime: &ComposeRuntime,
    args: &[&str],
    action: &str,
) -> Result<String, String> {
    let output = compose_command(runtime)
        .args(args)
        .output()
        .map_err(|e| format!("{action}: {e}"))?;
    let mut bytes = output.stdout;
    bytes.extend_from_slice(&output.stderr);
    let message = String::from_utf8_lossy(&bytes).trim().to_string();
    if output.status.success() {
        Ok(message)
    } else if message.is_empty() {
        Err(format!("{action} failed"))
    } else {
        Err(format!("{action}: {}", bounded(&message)))
    }
}

fn compose_stream_up(
    runtime: &ComposeRuntime,
    manager: &PreviewManager,
    project_id: &str,
    generation: u64,
) -> Result<(), String> {
    let mut child = compose_command(runtime)
        .args(["--progress", "plain", "up", "-d", "--build"])
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| format!("start preview stack: {e}"))?;
    let (sender, receiver) = mpsc::channel();
    if let Some(stdout) = child.stdout.take() {
        spawn_log_reader(stdout, sender.clone());
    }
    if let Some(stderr) = child.stderr.take() {
        spawn_log_reader(stderr, sender.clone());
    }
    drop(sender);
    for line in receiver {
        if !line.trim().is_empty() {
            manager.append_log(project_id, generation, format!("[preview] {line}"));
        }
    }
    let status = child
        .wait()
        .map_err(|e| format!("wait for preview stack startup: {e}"))?;
    if status.success() {
        Ok(())
    } else {
        Err("start preview stack failed; see preview logs for details".into())
    }
}

fn spawn_log_reader<R: Read + Send + 'static>(output: R, sender: mpsc::Sender<String>) {
    std::thread::spawn(move || {
        for line in BufReader::new(output).lines().map_while(Result::ok) {
            let _ = sender.send(line);
        }
    });
}

fn compose_down(runtime: &ComposeRuntime, volumes: bool, images: bool) -> Result<(), String> {
    let args = compose_down_args(volumes, images);
    compose_capture(runtime, &args, "stop preview stack").map(|_| ())
}

fn compose_down_args(volumes: bool, images: bool) -> Vec<&'static str> {
    let mut args = vec!["down", "--remove-orphans"];
    if volumes {
        args.push("-v");
    }
    if images {
        args.extend(["--rmi", "local"]);
    }
    args
}

fn resolve_urls(
    runtime: &ComposeRuntime,
    ports: &[preview_compose::PublishedPort],
    open: &RunOpen,
) -> Result<Vec<PreviewUrl>, String> {
    let mut urls = Vec::with_capacity(ports.len());
    for port in ports {
        if port.protocol != "tcp" {
            continue;
        }
        let target = port.target.to_string();
        let args = compose_port_args(&port.protocol, &port.service, &target);
        let output = compose_capture(runtime, &args, "resolve preview port")?;
        let host_port = parse_published_port(&output)?;
        urls.push(PreviewUrl {
            service: port.service.clone(),
            url: format!("http://127.0.0.1:{host_port}"),
            open: port.service == open.service && port.target == open.port,
        });
    }
    Ok(urls)
}

fn compose_port_args<'a>(protocol: &'a str, service: &'a str, target: &'a str) -> [&'a str; 5] {
    ["port", "--protocol", protocol, service, target]
}

fn wait_for_http(
    manager: &PreviewManager,
    project_id: &str,
    generation: u64,
    runtime: &ComposeRuntime,
    open_service: &str,
    url: &str,
) -> Result<(), String> {
    let port = url
        .rsplit_once(':')
        .and_then(|(_, value)| value.parse::<u16>().ok())
        .ok_or_else(|| "resolved preview URL has no valid port".to_string())?;
    let deadline = Instant::now() + READY_TIMEOUT;
    while Instant::now() < deadline {
        if !manager.active(project_id, generation, PreviewState::Starting) {
            return Err("preview start was cancelled".into());
        }
        if let Some(error) = stack_failure(runtime, open_service)? {
            return Err(error);
        }
        if http_responds(port) {
            return Ok(());
        }
        std::thread::sleep(Duration::from_millis(500));
    }
    Err("the preview HTTP port did not become ready within 10 minutes".into())
}

fn http_responds(port: u16) -> bool {
    let address = SocketAddr::from(([127, 0, 0, 1], port));
    let Ok(mut stream) = TcpStream::connect_timeout(&address, Duration::from_millis(500)) else {
        return false;
    };
    let _ = stream.set_read_timeout(Some(Duration::from_secs(1)));
    if stream
        .write_all(b"GET / HTTP/1.0\r\nHost: 127.0.0.1\r\n\r\n")
        .is_err()
    {
        return false;
    }
    let mut prefix = [0_u8; 5];
    stream.read_exact(&mut prefix).is_ok() && &prefix == b"HTTP/"
}

fn watch_stack(
    app: AppHandle,
    manager: PreviewManager,
    project_id: String,
    generation: u64,
    runtime: ComposeRuntime,
    open_service: String,
) {
    std::thread::spawn(move || loop {
        std::thread::sleep(Duration::from_secs(1));
        if !manager.active(&project_id, generation, PreviewState::Running) {
            return;
        }
        match stack_failure(&runtime, &open_service) {
            Ok(None) => continue,
            Ok(Some(error)) => {
                fail_preview(&app, &manager, &project_id, generation, &error, None);
                return;
            }
            Err(error) => {
                fail_preview(&app, &manager, &project_id, generation, &error, None);
                return;
            }
        }
    });
}

fn stack_failure(runtime: &ComposeRuntime, open_service: &str) -> Result<Option<String>, String> {
    let output = compose_capture(
        runtime,
        &["ps", "--all", "--format", "json"],
        "inspect preview stack",
    )?;
    let services = parse_compose_ps(&output)?;
    if services.iter().any(|service| service.exit_code != 0) {
        return Ok(Some("a preview service exited with an error".into()));
    }
    let open_running = services
        .iter()
        .any(|service| service.service == open_service && service.state == "running");
    if !open_running {
        return Ok(Some("the preview open service stopped".into()));
    }
    Ok(None)
}

#[derive(Deserialize)]
#[serde(rename_all = "PascalCase")]
struct ComposeServiceState {
    service: String,
    state: String,
    #[serde(default)]
    exit_code: i64,
}

fn parse_compose_ps(output: &str) -> Result<Vec<ComposeServiceState>, String> {
    if let Ok(services) = serde_json::from_str::<Vec<ComposeServiceState>>(output) {
        return Ok(services);
    }
    let services = output
        .lines()
        .filter(|line| !line.trim().is_empty())
        .map(serde_json::from_str::<ComposeServiceState>)
        .collect::<Result<Vec<_>, _>>()
        .map_err(|e| format!("decode preview service status: {e}"))?;
    if services.is_empty() {
        return Err("the preview stack has no containers".into());
    }
    Ok(services)
}

fn fail_preview(
    app: &AppHandle,
    manager: &PreviewManager,
    project_id: &str,
    generation: u64,
    error: &str,
    fallback_runtime: Option<ComposeRuntime>,
) {
    if let Some((status, runtime)) = manager.fail(project_id, generation, &bounded(error)) {
        let _ = app.emit(STATUS_EVENT, status);
        if let Some(runtime) = runtime.or(fallback_runtime) {
            let _ = compose_down(&runtime, false, false);
            remove_source(&runtime.source_root);
        }
    } else if let Some(runtime) = fallback_runtime {
        let _ = compose_down(&runtime, false, false);
        remove_source(&runtime.source_root);
    }
}

fn compose_logs(runtime: &ComposeRuntime) -> Result<Vec<String>, String> {
    let tail = LOG_LIMIT.to_string();
    let output = compose_capture(
        runtime,
        &["logs", "--tail", &tail, "--no-color"],
        "read preview logs",
    )?;
    Ok(output.lines().map(str::to_string).collect())
}

fn remove_source(path: &Path) {
    let _ = fs::remove_dir_all(path);
}

pub(crate) fn sweep_leftovers() {
    sweep_source_directories();
    let Ok(output) = docker::docker_command()
        .args([
            "ps",
            "-a",
            "--filter",
            &format!("label={COMPOSE_PROJECT_LABEL}"),
            "--format",
            &format!("{{{{.Label \"{COMPOSE_PROJECT_LABEL}\"}}}}"),
        ])
        .output()
    else {
        return;
    };
    if !output.status.success() {
        return;
    }
    let projects = String::from_utf8_lossy(&output.stdout)
        .lines()
        .filter(|name| name.starts_with(PREVIEW_TEMP_PREFIX))
        .map(str::to_string)
        .collect::<HashSet<_>>();
    for project in projects {
        remove_labeled_resources("container", &project);
        remove_labeled_resources("network", &project);
    }
}

fn remove_labeled_resources(kind: &str, project: &str) {
    let Ok(output) = docker::docker_command()
        .args([
            kind,
            "ls",
            "-q",
            "--filter",
            &format!("label={COMPOSE_PROJECT_LABEL}={project}"),
        ])
        .output()
    else {
        return;
    };
    if !output.status.success() {
        return;
    }
    let identifiers = String::from_utf8_lossy(&output.stdout)
        .split_whitespace()
        .map(str::to_string)
        .collect::<Vec<_>>();
    if identifiers.is_empty() {
        return;
    }
    let mut command = docker::docker_command();
    command.arg(kind).arg("rm");
    if kind == "container" {
        command.arg("--force");
    }
    let _ = command.args(identifiers).output();
}

fn sweep_source_directories() {
    let Ok(entries) = fs::read_dir(std::env::temp_dir()) else {
        return;
    };
    for entry in entries.flatten() {
        let name = entry.file_name();
        let Some(name) = name.to_str() else { continue };
        if !name.starts_with(PREVIEW_TEMP_PREFIX) {
            continue;
        }
        let path = entry.path();
        match fs::symlink_metadata(&path) {
            Ok(metadata) if metadata.file_type().is_symlink() => {
                let _ = fs::remove_file(path);
            }
            Ok(metadata) if metadata.is_dir() => {
                let _ = fs::remove_dir_all(path);
            }
            _ => {}
        }
    }
}

pub(crate) fn stop_all(manager: &PreviewManager) {
    let projects = manager
        .inner
        .lock()
        .ok()
        .map(|registry| registry.projects.keys().cloned().collect::<Vec<_>>())
        .unwrap_or_default();
    for project in projects {
        if let Ok(Some(stopped)) = manager.stop(&project) {
            if let Some(runtime) = stopped.runtime {
                let _ = compose_down(&runtime, false, false);
                remove_source(&runtime.source_root);
            }
        }
    }
    sweep_leftovers();
}

async fn coordinator_json<T: for<'de> Deserialize<'de>>(
    response: reqwest::Response,
    name: &str,
) -> Result<T, String> {
    let status = response.status();
    if !status.is_success() {
        let body = response.text().await.unwrap_or_default();
        if let Ok(value) = serde_json::from_str::<Value>(&body) {
            if let Some(message) = value.pointer("/error/message").and_then(Value::as_str) {
                return Err(message.to_string());
            }
        }
        return Err(format!("coordinator returned HTTP {status} for {name}"));
    }
    response
        .json()
        .await
        .map_err(|e| format!("decode {name}: {e}"))
}

fn coordinator_url(path: &str) -> String {
    let port = std::env::var("COMMITARIUM_COORDINATOR_PORT").unwrap_or_else(|_| "8080".into());
    format!("http://127.0.0.1:{port}{path}")
}

fn internal_repository_url(repository: &Repository) -> Result<String, String> {
    validate_coordinate("repository owner", &repository.owner)?;
    validate_coordinate("repository name", &repository.name)?;
    let port = std::env::var("COMMITARIUM_FORGEJO_PORT").unwrap_or_else(|_| "3001".into());
    Ok(format!(
        "http://127.0.0.1:{port}/{}/{}.git",
        repository.owner, repository.name
    ))
}

fn forgejo_token_path() -> Result<PathBuf, String> {
    if let Ok(configured) = std::env::var("COMMITARIUM_FORGEJO_TOKEN_FILE") {
        let path = PathBuf::from(configured);
        return path
            .is_file()
            .then_some(path)
            .ok_or_else(|| "COMMITARIUM_FORGEJO_TOKEN_FILE does not name a file".into());
    }
    if let Ok(compose) = std::env::var("COMMITARIUM_COMPOSE_FILE") {
        if let Some(parent) = Path::new(&compose).parent() {
            let candidate = parent.join(".commitarium/forgejo-token");
            if candidate.is_file() {
                return Ok(candidate);
            }
        }
    }
    let mut directory = std::env::current_dir().map_err(|e| format!("current directory: {e}"))?;
    loop {
        let candidate = directory.join(".commitarium/forgejo-token");
        if candidate.is_file() {
            return Ok(candidate);
        }
        if !directory.pop() {
            return Err(
                "internal Forgejo token not found; set COMMITARIUM_FORGEJO_TOKEN_FILE".into(),
            );
        }
    }
}

fn validate_handoff(handoff: &ProjectHandoff, project_id: &str) -> Result<(), String> {
    if handoff.project_id != project_id {
        return Err("coordinator returned a handoff for another project".into());
    }
    validate_coordinate("repository owner", &handoff.source.repository.owner)?;
    validate_coordinate("repository name", &handoff.source.repository.name)?;
    if handoff.source.default_branch.is_empty()
        || handoff.source.default_branch.len() > 256
        || handoff.source.default_branch.contains('\0')
    {
        return Err("canonical branch is invalid".into());
    }
    if !safe_commit(&handoff.source.head_commit_id) {
        return Err("canonical head is not a valid Git object ID".into());
    }
    Ok(())
}

fn validate_run(run: &RunConfig) -> Result<(), String> {
    if run.open.port == 0 || !safe_service_name(&run.open.service) {
        return Err("preview open target is invalid".into());
    }
    Ok(())
}

fn safe_service_name(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 63
        && value.bytes().enumerate().all(|(index, byte)| {
            if index == 0 {
                byte.is_ascii_alphanumeric()
            } else {
                byte.is_ascii_alphanumeric() || matches!(byte, b'_' | b'.' | b'-')
            }
        })
}

fn validate_identifier(name: &str, value: &str) -> Result<(), String> {
    if value.is_empty()
        || value.len() > 80
        || !value
            .bytes()
            .all(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit() || b"_-".contains(&byte))
        || !value.as_bytes()[0].is_ascii_alphanumeric()
    {
        return Err(format!("{name} is invalid"));
    }
    Ok(())
}

fn validate_coordinate(name: &str, value: &str) -> Result<(), String> {
    if value.is_empty()
        || value.len() > 128
        || matches!(value, "." | "..")
        || !value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'.' | b'_' | b'-'))
    {
        return Err(format!("{name} is invalid"));
    }
    Ok(())
}

fn safe_commit(value: &str) -> bool {
    matches!(value.len(), 40 | 64)
        && value
            .bytes()
            .all(|byte| byte.is_ascii_hexdigit() && !byte.is_ascii_uppercase())
}

fn parse_published_port(output: &str) -> Result<u16, String> {
    output
        .lines()
        .find_map(|line| {
            line.trim()
                .strip_prefix("127.0.0.1:")
                .and_then(|port| port.parse().ok())
        })
        .ok_or_else(|| "Docker did not publish the preview port on loopback".to_string())
}

fn run_checked(command: &mut Command, action: &str) -> Result<(), String> {
    let output = command.output().map_err(|e| format!("{action}: {e}"))?;
    if output.status.success() {
        Ok(())
    } else {
        Err(format!(
            "{action}: {}",
            bounded(&String::from_utf8_lossy(&output.stderr))
        ))
    }
}

fn command_line(command: &mut Command, action: &str) -> Result<String, String> {
    let output = command.output().map_err(|e| format!("{action}: {e}"))?;
    if output.status.success() {
        Ok(String::from_utf8_lossy(&output.stdout).trim().to_string())
    } else {
        Err(format!(
            "{action}: {}",
            bounded(&String::from_utf8_lossy(&output.stderr))
        ))
    }
}

fn bounded(value: &str) -> String {
    value.chars().take(4000).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn published_ports_must_be_ipv4_loopback_host_ports() {
        assert_eq!(parse_published_port("127.0.0.1:49152\n").unwrap(), 49152);
        assert!(parse_published_port("0.0.0.0:49152").is_err());
        assert!(parse_published_port("not a mapping").is_err());
    }

    #[test]
    fn run_validation_matches_the_coordinator_contract() {
        assert!(validate_run(&RunConfig {
            open: RunOpen {
                service: "web.v2_api-1".into(),
                port: 5173,
            }
        })
        .is_ok());
        assert!(validate_run(&RunConfig {
            open: RunOpen {
                service: "bad service".into(),
                port: 5173,
            }
        })
        .is_err());
        assert!(validate_run(&RunConfig {
            open: RunOpen {
                service: "web".into(),
                port: 0,
            }
        })
        .is_err());
    }

    #[test]
    fn compose_ps_accepts_array_and_line_delimited_json() {
        let array = r#"[{"Service":"web","State":"running","ExitCode":0}]"#;
        assert_eq!(parse_compose_ps(array).unwrap()[0].service, "web");
        let lines = "{\"Service\":\"web\",\"State\":\"running\",\"ExitCode\":0}\n{\"Service\":\"job\",\"State\":\"exited\",\"ExitCode\":0}\n";
        assert_eq!(parse_compose_ps(lines).unwrap().len(), 2);
    }

    #[test]
    fn destructive_cleanup_uses_explicit_compose_flags() {
        assert_eq!(
            compose_down_args(false, false),
            ["down", "--remove-orphans"]
        );
        assert_eq!(
            compose_down_args(true, false),
            ["down", "--remove-orphans", "-v"]
        );
        assert_eq!(
            compose_down_args(true, true),
            ["down", "--remove-orphans", "-v", "--rmi", "local"]
        );
    }

    #[test]
    fn compose_port_uses_a_bare_port_and_explicit_protocol_flag() {
        assert_eq!(
            compose_port_args("tcp", "frontend", "5173"),
            ["port", "--protocol", "tcp", "frontend", "5173"]
        );
    }
}
