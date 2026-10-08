//! Disposable project previews for ADR-013.
//!
//! The renderer supplies only a project identity and target. This module reads
//! the trusted coordinator contract, fetches the canonical commit with the
//! host-only Forgejo credential, and launches a resource-bounded container.

use crate::docker;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::{HashMap, VecDeque};
use std::fs;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{SocketAddr, TcpStream};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};
use tauri::{AppHandle, Emitter, State};

const STATUS_EVENT: &str = "preview-status-changed";
const PREVIEW_LABEL: &str = "commitarium.preview";
const PREVIEW_TEMP_PREFIX: &str = "commitarium-preview-";
const READY_TIMEOUT: Duration = Duration::from_secs(10 * 60);
const LOG_LIMIT: usize = 400;

const PREVIEW_SCRIPT: &str = r#"set -eu
mkdir -p /workspace /tmp/home /tmp/mise-cache /tmp/mise-state
cp -a /source/. /workspace/
cd /workspace

setup_count=$(jq '.setup | length' /spec/run.json)
index=0
while [ "$index" -lt "$setup_count" ]; do
    command=$(jq -r ".setup[$index]" /spec/run.json)
    fifo="/tmp/preview-setup-$index"
    mkfifo "$fifo"
    sed -u 's/^/[setup] /' < "$fifo" &
    logger=$!
    set +e
    /bin/sh -lc "$command" > "$fifo" 2>&1
    code=$?
    set -e
    wait "$logger"
    rm -f "$fifo"
    if [ "$code" -ne 0 ]; then exit "$code"; fi
    index=$((index+1))
done

pids=''
loggers=''
cleanup() {
    for pid in $pids $loggers; do kill "$pid" 2>/dev/null || true; done
    wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

process_count=$(jq '.processes | length' /spec/run.json)
index=0
while [ "$index" -lt "$process_count" ]; do
    name=$(jq -r ".processes[$index].name" /spec/run.json)
    command=$(jq -r ".processes[$index].command" /spec/run.json)
    fifo="/tmp/preview-process-$index"
    mkfifo "$fifo"
    sed -u "s/^/[$name] /" < "$fifo" &
    logger=$!
    /bin/sh -lc "$command" > "$fifo" 2>&1 &
    pid=$!
    pids="$pids $pid"
    loggers="$loggers $logger"
    index=$((index+1))
done

while :; do
    for pid in $pids; do
        if ! kill -0 "$pid" 2>/dev/null; then
            set +e
            wait "$pid"
            code=$?
            set -e
            exit "$code"
        fi
    done
    sleep 1
done
"#;

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
    process: String,
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

#[derive(Clone, Debug, Deserialize, Serialize)]
struct RunConfig {
    setup: Vec<String>,
    processes: Vec<RunProcess>,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct RunProcess {
    name: String,
    command: String,
    port: Option<u16>,
    #[serde(default)]
    open: bool,
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

struct PreviewRecord {
    status: PreviewStatus,
    generation: u64,
    container_id: Option<String>,
    source_root: Option<PathBuf>,
    logs: VecDeque<String>,
}

struct StoppedPreview {
    status: PreviewStatus,
    container_id: Option<String>,
    source_root: Option<PathBuf>,
}

struct PreviewLogSource {
    container_id: Option<String>,
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
                container_id: None,
                source_root: None,
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

    fn attach(
        &self,
        project_id: &str,
        generation: u64,
        container_id: String,
        source_root: PathBuf,
    ) -> bool {
        let Ok(mut registry) = self.inner.lock() else {
            return false;
        };
        let Some(record) = registry.projects.get_mut(project_id) else {
            return false;
        };
        if record.generation != generation || record.status.state != PreviewState::Starting {
            return false;
        }
        record.container_id = Some(container_id);
        record.source_root = Some(source_root);
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
    ) -> Option<(PreviewStatus, Option<PathBuf>)> {
        let mut registry = self.inner.lock().ok()?;
        let record = registry.projects.get_mut(project_id)?;
        if record.generation != generation || record.status.state == PreviewState::Stopped {
            return None;
        }
        let detail = if record.logs.is_empty() {
            fallback.to_string()
        } else {
            record
                .logs
                .iter()
                .rev()
                .take(20)
                .cloned()
                .collect::<Vec<_>>()
                .into_iter()
                .rev()
                .collect::<Vec<_>>()
                .join("\n")
        };
        record.status.state = PreviewState::Failed;
        record.status.error = Some(detail);
        record.status.urls.clear();
        record.container_id = None;
        Some((record.status.clone(), record.source_root.take()))
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
            container_id: record.container_id.take(),
            source_root: record.source_root.take(),
        }))
    }

    fn container_and_logs(&self, project_id: &str) -> Result<Option<PreviewLogSource>, String> {
        let registry = self
            .inner
            .lock()
            .map_err(|_| "preview state is unavailable")?;
        Ok(registry
            .projects
            .get(project_id)
            .map(|record| PreviewLogSource {
                container_id: record.container_id.clone(),
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
        .ok_or("this project's stack has no preview run configuration")?;
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
            // A panic is deliberately reduced to a non-sensitive fixed error.
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
    let Some(source) = manager.container_and_logs(&project_id)? else {
        return Ok(Vec::new());
    };
    let Some(container) = source.container_id else {
        return Ok(source.cached);
    };
    let logs = tauri::async_runtime::spawn_blocking(move || docker_logs(&container))
        .await
        .map_err(|_| "preview log task stopped unexpectedly".to_string())?;
    match logs {
        Ok(lines) if !lines.is_empty() => Ok(lines),
        _ => Ok(source.cached),
    }
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
        if let Some(container) = stopped.container_id {
            remove_container(&container);
        }
        if let Some(path) = stopped.source_root {
            let _ = fs::remove_dir_all(path);
        }
    })
    .await
    .map_err(|_| "preview stop task stopped unexpectedly".to_string())?;
    Ok(())
}

fn launch_preview(
    app: AppHandle,
    manager: PreviewManager,
    project_id: String,
    generation: u64,
    handoff: ProjectHandoff,
    run: RunConfig,
) {
    let root = match prepare_source(&handoff, &run) {
        Ok(path) => path,
        Err(error) => {
            fail_preview(&app, &manager, &project_id, generation, &error, None);
            return;
        }
    };
    let launched = launch_container(&project_id, generation, &root, &run);
    let container = match launched {
        Ok(container) => container,
        Err(error) => {
            fail_preview(&app, &manager, &project_id, generation, &error, Some(root));
            return;
        }
    };
    if !manager.attach(&project_id, generation, container.clone(), root.clone()) {
        remove_container(&container);
        let _ = fs::remove_dir_all(root);
        return;
    }
    follow_logs(
        manager.clone(),
        project_id.clone(),
        generation,
        container.clone(),
    );
    watch_container(
        app.clone(),
        manager.clone(),
        project_id.clone(),
        generation,
        container.clone(),
    );

    let urls = match resolve_urls(&container, &run) {
        Ok(urls) => urls,
        Err(error) => {
            fail_preview(&app, &manager, &project_id, generation, &error, None);
            remove_container(&container);
            return;
        }
    };
    if let Some(open) = urls.iter().find(|url| url.open) {
        if let Err(error) = wait_for_http(&manager, &project_id, generation, &container, &open.url)
        {
            fail_preview(&app, &manager, &project_id, generation, &error, None);
            remove_container(&container);
            return;
        }
    }
    if let Some(status) = manager.running(&project_id, generation, urls) {
        let _ = app.emit(STATUS_EVENT, status);
    }
}

fn prepare_source(handoff: &ProjectHandoff, run: &RunConfig) -> Result<PathBuf, String> {
    let temporary = tempfile::Builder::new()
        .prefix(PREVIEW_TEMP_PREFIX)
        .tempdir()
        .map_err(|e| format!("create preview source directory: {e}"))?;
    let root = temporary.path().to_path_buf();
    let source = root.join("source");
    let spec = root.join("spec");
    fs::create_dir_all(&source).map_err(|e| format!("create preview checkout: {e}"))?;
    fs::create_dir_all(&spec).map_err(|e| format!("create preview specification: {e}"))?;
    fs::write(
        spec.join("run.json"),
        serde_json::to_vec(run).map_err(|e| e.to_string())?,
    )
    .map_err(|e| format!("write preview specification: {e}"))?;
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

fn launch_container(
    project_id: &str,
    generation: u64,
    root: &Path,
    run: &RunConfig,
) -> Result<String, String> {
    let image = docker::configured_service_image("codex-worker")?;
    let toolchain_volume = format!("{}_commitarium-toolchains", docker::PROJECT_NAME);
    let name = container_name(project_id, generation);
    let mut command = docker::docker_command();
    command
        .args([
            "run",
            "-d",
            "--rm",
            "--pull=never",
            "--name",
            &name,
            "--label",
        ])
        .arg(format!("{PREVIEW_LABEL}={project_id}"))
        .args([
            "--cpus",
            "2",
            "--memory",
            "4g",
            "--pids-limit",
            "512",
            "--read-only",
        ])
        .args([
            "--tmpfs",
            "/workspace:rw,exec,nosuid,nodev,size=4g,uid=65532,gid=65532",
        ])
        .args([
            "--tmpfs",
            "/tmp:rw,exec,nosuid,nodev,size=1g,uid=65532,gid=65532",
        ])
        .arg("--mount")
        .arg(format!(
            "type=bind,src={},dst=/source,readonly",
            root.join("source").display()
        ))
        .arg("--mount")
        .arg(format!(
            "type=bind,src={},dst=/spec,readonly",
            root.join("spec").display()
        ))
        .arg("--mount")
        .arg(format!(
            "type=volume,src={toolchain_volume},dst=/toolchains,readonly"
        ))
        .args([
            "--env",
            "HOME=/tmp/home",
            "--env",
            "PATH=/toolchains/data/shims:/usr/local/bin:/usr/bin:/bin",
        ])
        .arg("--env")
        .arg(format!(
            "MISE_GLOBAL_CONFIG_FILE=/toolchains/projects/{project_id}/mise.toml"
        ))
        .args([
            "--env",
            "MISE_SAFE=1",
            "--env",
            "MISE_DATA_DIR=/toolchains/data",
            "--env",
            "MISE_CACHE_DIR=/tmp/mise-cache",
            "--env",
            "MISE_STATE_DIR=/tmp/mise-state",
        ])
        .args(["--cap-drop", "ALL", "--security-opt", "no-new-privileges"]);
    for process in &run.processes {
        if let Some(port) = process.port {
            command.args(["--publish", &format!("127.0.0.1::{port}")]);
        }
    }
    command.args([
        "--entrypoint",
        "/usr/bin/timeout",
        &image,
        "24h",
        "/bin/sh",
        "-c",
        PREVIEW_SCRIPT,
    ]);
    command_line(&mut command, "launch preview container")
}

fn resolve_urls(container: &str, run: &RunConfig) -> Result<Vec<PreviewUrl>, String> {
    let default_open = run
        .processes
        .iter()
        .position(|process| process.open)
        .or_else(|| {
            run.processes
                .iter()
                .position(|process| process.port.is_some())
        });
    let mut urls = Vec::new();
    for (index, process) in run.processes.iter().enumerate() {
        let Some(port) = process.port else { continue };
        let output = command_line(
            docker::docker_command().args(["port", container, &format!("{port}/tcp")]),
            "resolve preview port",
        )?;
        let host_port = parse_published_port(&output)?;
        urls.push(PreviewUrl {
            process: process.name.clone(),
            url: format!("http://127.0.0.1:{host_port}"),
            open: Some(index) == default_open,
        });
    }
    Ok(urls)
}

fn wait_for_http(
    manager: &PreviewManager,
    project_id: &str,
    generation: u64,
    container: &str,
    url: &str,
) -> Result<(), String> {
    let port = url
        .rsplit_once(':')
        .and_then(|(_, value)| value.parse::<u16>().ok())
        .ok_or_else(|| "resolved preview URL has no valid port".to_string())?;
    let deadline = Instant::now() + READY_TIMEOUT;
    while Instant::now() < deadline {
        let status = manager.status(project_id)?;
        if status
            .as_ref()
            .is_none_or(|status| status.state != PreviewState::Starting)
        {
            return Err("preview start was cancelled".into());
        }
        if !container_running(container) {
            return Err("a preview process exited before its HTTP port became ready".into());
        }
        if http_responds(port) {
            return Ok(());
        }
        std::thread::sleep(Duration::from_millis(500));
    }
    let _ = generation;
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

fn follow_logs(manager: PreviewManager, project_id: String, generation: u64, container: String) {
    std::thread::spawn(move || {
        let mut command = docker::docker_command();
        command
            .args([
                "logs",
                "--follow",
                "--tail",
                &LOG_LIMIT.to_string(),
                &container,
            ])
            .stdout(Stdio::piped())
            .stderr(Stdio::null());
        let Ok(mut child) = command.spawn() else {
            return;
        };
        if let Some(stdout) = child.stdout.take() {
            for line in BufReader::new(stdout).lines().map_while(Result::ok) {
                manager.append_log(&project_id, generation, line);
            }
        }
        let _ = child.wait();
    });
}

fn watch_container(
    app: AppHandle,
    manager: PreviewManager,
    project_id: String,
    generation: u64,
    container: String,
) {
    std::thread::spawn(move || {
        let mut command = docker::docker_command();
        let waited = command.args(["wait", &container]).output();
        if !waited.is_ok_and(|output| output.status.success()) {
            while container_running(&container) {
                std::thread::sleep(Duration::from_secs(1));
            }
        }
        std::thread::sleep(Duration::from_millis(100));
        fail_preview(
            &app,
            &manager,
            &project_id,
            generation,
            "a preview process exited",
            None,
        );
    });
}

fn fail_preview(
    app: &AppHandle,
    manager: &PreviewManager,
    project_id: &str,
    generation: u64,
    error: &str,
    source: Option<PathBuf>,
) {
    if let Some((status, managed_source)) = manager.fail(project_id, generation, &bounded(error)) {
        let _ = app.emit(STATUS_EVENT, status);
        if let Some(path) = managed_source.or(source) {
            let _ = fs::remove_dir_all(path);
        }
    } else if let Some(path) = source {
        let _ = fs::remove_dir_all(path);
    }
}

fn docker_logs(container: &str) -> Result<Vec<String>, String> {
    let output = docker::docker_command()
        .args(["logs", "--tail", &LOG_LIMIT.to_string(), container])
        .output()
        .map_err(|e| format!("read preview logs: {e}"))?;
    if !output.status.success() {
        return Err("preview logs are no longer available".into());
    }
    let mut bytes = output.stdout;
    bytes.extend_from_slice(&output.stderr);
    Ok(String::from_utf8_lossy(&bytes)
        .lines()
        .map(str::to_string)
        .collect())
}

fn remove_container(container: &str) {
    let _ = docker::docker_command()
        .args(["rm", "--force", container])
        .output();
}

fn container_running(container: &str) -> bool {
    docker::docker_command()
        .args(["inspect", "--format", "{{.State.Running}}", container])
        .output()
        .is_ok_and(|output| {
            output.status.success() && String::from_utf8_lossy(&output.stdout).trim() == "true"
        })
}

pub(crate) fn sweep_leftovers() {
    sweep_source_directories();
    let Ok(output) = docker::docker_command()
        .args(["ps", "-aq", "--filter", &format!("label={PREVIEW_LABEL}")])
        .output()
    else {
        return;
    };
    if !output.status.success() {
        return;
    }
    for container in String::from_utf8_lossy(&output.stdout).split_whitespace() {
        remove_container(container);
    }
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
            if let Some(container) = stopped.container_id {
                remove_container(&container);
            }
            if let Some(path) = stopped.source_root {
                let _ = fs::remove_dir_all(path);
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
        if let Ok(value) = serde_json::from_str::<serde_json::Value>(&body) {
            if let Some(message) = value
                .pointer("/error/message")
                .and_then(serde_json::Value::as_str)
            {
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
    if run.processes.is_empty() {
        return Err("preview run configuration has no processes".into());
    }
    let mut names = std::collections::HashSet::new();
    let mut ports = std::collections::HashSet::new();
    let mut open = 0;
    for command in &run.setup {
        validate_command(command)?;
    }
    for process in &run.processes {
        if !safe_process_name(&process.name) || !names.insert(process.name.as_str()) {
            return Err(
                "preview run configuration has an invalid or duplicate process name".into(),
            );
        }
        validate_command(&process.command)?;
        if let Some(port) = process.port {
            if port == 0 || !ports.insert(port) {
                return Err("preview run configuration has an invalid or duplicate port".into());
            }
        }
        if process.open {
            open += 1;
            if process.port.is_none() {
                return Err("the open preview process requires a port".into());
            }
        }
    }
    if open > 1 {
        return Err("preview run configuration opens more than one process".into());
    }
    Ok(())
}

fn validate_command(command: &str) -> Result<(), String> {
    if command.is_empty() || command != command.trim() || command.contains('\0') {
        return Err("preview run configuration contains an invalid command".into());
    }
    Ok(())
}

fn safe_process_name(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 32
        && value.bytes().enumerate().all(|(index, byte)| {
            if index == 0 {
                byte.is_ascii_lowercase()
            } else {
                byte.is_ascii_lowercase() || byte.is_ascii_digit() || byte == b'-'
            }
        })
}

fn validate_identifier(name: &str, value: &str) -> Result<(), String> {
    if value.is_empty()
        || value.len() > 160
        || !value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || b"._:-".contains(&byte))
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

fn container_name(project_id: &str, generation: u64) -> String {
    let mut digest = Sha256::new();
    digest.update(project_id.as_bytes());
    digest.update(generation.to_be_bytes());
    let encoded = format!("{:x}", digest.finalize());
    format!("commitarium-preview-{}", &encoded[..16])
}

fn parse_published_port(output: &str) -> Result<u16, String> {
    output
        .lines()
        .find_map(|line| {
            line.trim()
                .rsplit_once(':')
                .and_then(|(_, port)| port.parse().ok())
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
    fn published_ports_are_loopback_host_ports() {
        assert_eq!(parse_published_port("127.0.0.1:49152\n").unwrap(), 49152);
        assert!(parse_published_port("not a mapping").is_err());
    }

    #[test]
    fn run_validation_matches_the_coordinator_contract() {
        let run = RunConfig {
            setup: vec!["npm ci".into()],
            processes: vec![RunProcess {
                name: "web".into(),
                command: "npm run dev -- --host 0.0.0.0".into(),
                port: Some(5173),
                open: true,
            }],
        };
        assert!(validate_run(&run).is_ok());
        let duplicate = RunConfig {
            setup: vec![],
            processes: vec![
                run.processes[0].clone(),
                RunProcess {
                    name: "api".into(),
                    command: "serve".into(),
                    port: Some(5173),
                    open: false,
                },
            ],
        };
        assert!(validate_run(&duplicate).is_err());
        let leading_digit = RunConfig {
            setup: vec![],
            processes: vec![RunProcess {
                name: "1web".into(),
                command: "serve".into(),
                port: None,
                open: false,
            }],
        };
        assert!(validate_run(&leading_digit).is_err());
    }

    #[test]
    fn preview_script_prefixes_setup_and_process_logs() {
        assert!(PREVIEW_SCRIPT.contains("[setup]"));
        assert!(PREVIEW_SCRIPT.contains("[$name]"));
        assert!(PREVIEW_SCRIPT.contains("kill -0"));
    }
}
