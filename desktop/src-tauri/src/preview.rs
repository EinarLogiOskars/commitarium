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
pub(crate) const PREVIEW_TEMP_PREFIX: &str = "commitarium-preview-";
const COMPOSE_PROJECT_LABEL: &str = "com.docker.compose.project";
const READY_TIMEOUT: Duration = Duration::from_secs(10 * 60);
const LOG_LIMIT: usize = 400;

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum PreviewTarget {
    Canonical,
    /// A session's working copy, uncommitted changes included (ADR-019).
    Session {
        feature_id: String,
    },
}

/// Where a preview's sources come from.
enum PreviewSource {
    /// A fresh clone of the default branch head.
    Canonical(ProjectHandoff),
    /// A session's checkout, used in place and never removed.
    Checkout(PathBuf),
}

#[derive(Deserialize)]
struct SessionWorkspace {
    checkout: Option<SessionCheckout>,
}

#[derive(Deserialize)]
struct SessionCheckout {
    relative_path: String,
}

/// The registry key of one preview: a project's default branch, or one of its
/// sessions.
fn preview_key(project_id: &str, feature_id: Option<&str>) -> Result<String, String> {
    validate_identifier("project ID", project_id)?;
    match feature_id {
        None => Ok(project_id.to_string()),
        Some(feature_id) => {
            validate_record_id("work order ID", feature_id)?;
            Ok(format!("{project_id}#{feature_id}"))
        }
    }
}

/// The Compose project of a session's preview, apart from the default
/// branch's: lowercase, as Compose requires.
fn session_compose_id(project_id: &str, feature_id: &str) -> String {
    format!("{project_id}-{}", feature_id.to_ascii_lowercase())
}

/// The host directory holding the coordinator's managed checkouts.
fn workspaces_root() -> Result<PathBuf, String> {
    if let Ok(configured) = std::env::var("COMMITARIUM_WORKSPACE_SOURCE") {
        let path = PathBuf::from(&configured);
        if path.is_absolute() {
            return Ok(path);
        }
        if let Ok(compose) = std::env::var("COMMITARIUM_COMPOSE_FILE") {
            if let Some(parent) = Path::new(&compose).parent() {
                return Ok(parent.join(path));
            }
        }
    }
    let token = forgejo_token_path()?;
    token
        .parent()
        .map(|internal| internal.join("workspaces"))
        .ok_or_else(|| "the managed checkouts directory is unknown".into())
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

impl PreviewUrl {
    /// The loopback port of the URL the preview opens.
    pub(crate) fn open_port(&self) -> Option<u16> {
        if !self.open {
            return None;
        }
        self.url.rsplit_once(':')?.1.parse().ok()
    }
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
    /// What each container did, for a preview that failed after its
    /// containers were created (ADR-028).
    #[serde(skip_serializing_if = "Vec::is_empty")]
    services: Vec<ServiceEvidence>,
}

/// What one compose service did: its state, exit code, health, and last log
/// lines. The preview shows it when it fails, and the preview check reports
/// it to the coordinator.
#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub(crate) struct ServiceEvidence {
    pub(crate) service: String,
    pub(crate) state: String,
    pub(crate) exit_code: i64,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub(crate) health: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub(crate) logs: String,
}

/// The last log lines kept for each service.
pub(crate) const EVIDENCE_LOG_LINES: usize = 60;

#[derive(Clone, Debug, Deserialize)]
pub(crate) struct RunConfig {
    pub(crate) open: RunOpen,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub(crate) struct RunOpen {
    pub(crate) service: String,
    pub(crate) port: u16,
}

#[derive(Deserialize)]
pub(crate) struct ToolchainManifest {
    pub(crate) status: String,
    pub(crate) run: Option<RunConfig>,
    #[serde(default)]
    source: String,
    #[serde(default)]
    tools: serde_json::Map<String, serde_json::Value>,
    #[serde(default)]
    services: Vec<String>,
}

#[derive(Deserialize)]
pub(crate) struct ProjectHandoff {
    project_id: String,
    pub(crate) source: ProjectHandoffSource,
}

#[derive(Deserialize)]
pub(crate) struct ProjectHandoffSource {
    pub(crate) repository: Repository,
    default_branch: String,
    head_commit_id: String,
}

#[derive(Clone, Deserialize)]
pub(crate) struct Repository {
    owner: String,
    name: String,
}

#[derive(Clone)]
pub(crate) struct ComposeRuntime {
    pub(crate) project_name: String,
    pub(crate) compose_file: PathBuf,
    pub(crate) environment_file: PathBuf,
    pub(crate) source_root: PathBuf,
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
    fn begin(&self, key: &str, status: PreviewStatus) -> Result<u64, String> {
        let mut registry = self
            .inner
            .lock()
            .map_err(|_| "preview state is unavailable")?;
        registry.generation += 1;
        let generation = registry.generation;
        registry.projects.insert(
            key.to_string(),
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

    /// The runtime of a preview that is still starting or running.
    fn runtime(&self, project_id: &str, generation: u64) -> Option<ComposeRuntime> {
        let registry = self.inner.lock().ok()?;
        let record = registry.projects.get(project_id)?;
        (record.generation == generation && record.status.state != PreviewState::Stopped)
            .then(|| record.runtime.clone())
            .flatten()
    }

    fn fail(
        &self,
        project_id: &str,
        generation: u64,
        fallback: &str,
        services: Vec<ServiceEvidence>,
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
        record.status.services = services;
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
        record.status.services.clear();
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
    let feature_id = match &target {
        PreviewTarget::Canonical => None,
        PreviewTarget::Session { feature_id } => Some(feature_id.clone()),
    };
    let key = preview_key(&project_id, feature_id.as_deref())?;
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
    // Without an open target on the stack, the preview opens what the
    // Compose file declares, and the stack then remembers it.
    let run = manifest.run.clone();
    if let Some(run) = &run {
        validate_run(run)?;
    }
    let (source, commit_id, compose_id) = match &feature_id {
        None => {
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
            let commit_id = handoff.source.head_commit_id.clone();
            (
                PreviewSource::Canonical(handoff),
                commit_id,
                project_id.clone(),
            )
        }
        Some(feature_id) => {
            let workspace: SessionWorkspace = coordinator_json(
                client
                    .get(coordinator_url(&format!(
                        "/api/v1/projects/{project_id}/features/{feature_id}/workspace"
                    )))
                    .send()
                    .await
                    .map_err(|e| format!("request session workspace from coordinator: {e}"))?,
                "session workspace",
            )
            .await?;
            let checkout = workspace
                .checkout
                .ok_or("the session has no working copy yet")?;
            validate_record_id("checkout path", &checkout.relative_path)?;
            let path = workspaces_root()?
                .join(&checkout.relative_path)
                .canonicalize()
                .map_err(|e| format!("find the session's working copy: {e}"))?;
            (
                PreviewSource::Checkout(path),
                String::new(),
                session_compose_id(&project_id, feature_id),
            )
        }
    };

    let owned = manager.inner().clone();
    stop_project(&app, owned.clone(), &key).await?;
    let status = PreviewStatus {
        project_id: project_id.clone(),
        target,
        commit_id,
        state: PreviewState::Starting,
        urls: Vec::new(),
        error: None,
        services: Vec::new(),
    };
    let generation = owned.begin(&key, status.clone())?;
    let _ = app.emit(STATUS_EVENT, status.clone());
    let task_app = app.clone();
    tauri::async_runtime::spawn(async move {
        let launch_app = task_app.clone();
        let launch_manager = owned.clone();
        let launch_key = key.clone();
        let result = tauri::async_runtime::spawn_blocking(move || {
            launch_preview(
                launch_app,
                launch_manager,
                launch_key,
                generation,
                source,
                run,
                compose_id,
            )
        })
        .await;
        if let Ok(Some(open)) = &result {
            if let Err(error) = remember_open_target(&client, &project_id, &manifest, open).await {
                eprintln!("[preview] remember the preview open target: {error}");
            }
        }
        if result.is_err() {
            fail_preview(
                &task_app,
                &owned,
                &key,
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
    feature_id: Option<String>,
) -> Result<(), String> {
    let key = preview_key(&project_id, feature_id.as_deref())?;
    stop_project(&app, manager.inner().clone(), &key).await
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
    prune_sessions(app, manager.clone(), project_id, &[]).await?;
    let check = preview_compose::project_name(&crate::preview_check::check_compose_id(project_id))?;
    tauri::async_runtime::spawn_blocking(move || {
        for kind in ["container", "network", "volume", "image"] {
            remove_labeled_resources(kind, &check);
        }
    })
    .await
    .map_err(|_| "preview check cleanup stopped unexpectedly".to_string())?;
    cleanup_preview_data(app, manager, project_id, true).await
}

/// Removes the previews of the project's sessions that are no longer open:
/// their containers, networks, data, and images (ADR-019).
#[tauri::command]
pub async fn prune_session_previews(
    app: AppHandle,
    manager: State<'_, PreviewManager>,
    project_id: String,
    open_sessions: Vec<String>,
) -> Result<(), String> {
    validate_identifier("project ID", &project_id)?;
    for feature_id in &open_sessions {
        validate_record_id("work order ID", feature_id)?;
    }
    prune_sessions(&app, manager.inner().clone(), &project_id, &open_sessions).await
}

async fn prune_sessions(
    app: &AppHandle,
    manager: PreviewManager,
    project_id: &str,
    open_sessions: &[String],
) -> Result<(), String> {
    let mut keep: HashSet<String> = open_sessions
        .iter()
        .map(|feature_id| {
            preview_compose::project_name(&session_compose_id(project_id, feature_id))
        })
        .collect::<Result<_, _>>()?;
    // A running preview check is not a session; deleting the project removes
    // its leftovers itself.
    keep.insert(preview_compose::project_name(
        &crate::preview_check::check_compose_id(project_id),
    )?);
    // Running previews of closed sessions stop first.
    let prefix = format!("{project_id}#");
    let closed_keys = manager
        .inner
        .lock()
        .map_err(|_| "preview state is unavailable")?
        .projects
        .keys()
        .filter(|key| {
            key.strip_prefix(&prefix).is_some_and(|feature_id| {
                preview_compose::project_name(&session_compose_id(project_id, feature_id))
                    .is_ok_and(|name| !keep.contains(&name))
            })
        })
        .cloned()
        .collect::<Vec<_>>();
    for key in closed_keys {
        stop_project(app, manager.clone(), &key).await?;
    }
    let session_prefix = format!("{PREVIEW_TEMP_PREFIX}{project_id}-");
    tauri::async_runtime::spawn_blocking(move || {
        for project in compose_projects() {
            if !project.starts_with(&session_prefix) || keep.contains(&project) {
                continue;
            }
            for kind in ["container", "network", "volume", "image"] {
                remove_labeled_resources(kind, &project);
            }
        }
    })
    .await
    .map_err(|_| "preview cleanup stopped unexpectedly".to_string())
}

/// The Compose projects that own containers or volumes.
fn compose_projects() -> HashSet<String> {
    let mut projects = HashSet::new();
    for (kind, list) in [("container", ["ps", "-a"]), ("volume", ["volume", "ls"])] {
        let Ok(output) = docker::docker_command()
            .args(list)
            .args(["--filter", &format!("label={COMPOSE_PROJECT_LABEL}")])
            .args([
                "--format",
                &format!("{{{{.Label \"{COMPOSE_PROJECT_LABEL}\"}}}}"),
            ])
            .output()
        else {
            continue;
        };
        if !output.status.success() {
            eprintln!("[preview] list {kind} projects failed");
            continue;
        }
        projects.extend(
            String::from_utf8_lossy(&output.stdout)
                .lines()
                .map(str::trim)
                .filter(|name| !name.is_empty())
                .map(str::to_string),
        );
    }
    projects
}

#[tauri::command]
pub fn get_preview_status(
    manager: State<'_, PreviewManager>,
    project_id: String,
    feature_id: Option<String>,
) -> Result<Option<PreviewStatus>, String> {
    let key = preview_key(&project_id, feature_id.as_deref())?;
    manager.status(&key)
}

#[tauri::command]
pub async fn get_preview_logs(
    manager: State<'_, PreviewManager>,
    project_id: String,
    feature_id: Option<String>,
) -> Result<Vec<String>, String> {
    let key = preview_key(&project_id, feature_id.as_deref())?;
    let Some(source) = manager.logs(&key)? else {
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
    preview_source: PreviewSource,
    run: Option<RunConfig>,
    compose_id: String,
) -> Option<RunOpen> {
    // `project_id` here is the preview's registry key; `root` holds the
    // generated Compose files and is removed on stop. A session's working
    // copy is used in place and never removed.
    let prepared_root = match preview_source {
        PreviewSource::Canonical(handoff) => {
            prepare_commit_source(&handoff.source.repository, &handoff.source.head_commit_id)
                .map(|root| (root.clone(), root.join("source")))
        }
        PreviewSource::Checkout(checkout) => tempfile::Builder::new()
            .prefix(PREVIEW_TEMP_PREFIX)
            .tempdir()
            .map(|temporary| (temporary.keep(), checkout))
            .map_err(|e| format!("create preview directory: {e}")),
    };
    let (root, source) = match prepared_root {
        Ok(paths) => paths,
        Err(error) => {
            fail_preview(&app, &manager, &project_id, generation, &error, None);
            return None;
        }
    };
    let configured = run.as_ref().map(|run| preview_compose::OpenTarget {
        service: run.open.service.clone(),
        port: run.open.port,
    });
    let prepared =
        match preview_compose::normalize_and_rewrite(&source, &compose_id, configured.as_ref()) {
            Ok(prepared) => prepared,
            Err(error) => {
                fail_preview(&app, &manager, &project_id, generation, &error, None);
                remove_source(&root);
                return None;
            }
        };
    let compose_file = root.join("preview.compose.json");
    if let Err(error) = preview_compose::write_rewritten(&prepared, &compose_file) {
        fail_preview(&app, &manager, &project_id, generation, &error, None);
        remove_source(&root);
        return None;
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
        return None;
    }
    let runtime = ComposeRuntime {
        project_name: prepared.project_name.clone(),
        compose_file,
        environment_file,
        source_root: root,
    };
    if !manager.attach(&project_id, generation, runtime.clone()) {
        remove_source(&runtime.source_root);
        return None;
    }
    manager.append_log(
        &project_id,
        generation,
        "[preview] Building and starting the Compose stack.".into(),
    );
    match compose_stream_up(&runtime, &manager, &project_id, generation) {
        // A rebuild leaves the preview's previous images untagged.
        Ok(()) => crate::cleanup::spawn(),
        Err(error) => {
            manager.append_log(&project_id, generation, format!("[preview] {error}"));
            fail_preview(&app, &manager, &project_id, generation, &error, None);
            return None;
        }
    }
    if !manager.active(&project_id, generation, PreviewState::Starting) {
        let _ = compose_down(&runtime, false, false);
        remove_source(&runtime.source_root);
        return None;
    }

    let open = RunOpen {
        service: prepared.open.service.clone(),
        port: prepared.open.port,
    };
    let urls = match resolve_urls(&runtime, &prepared.published_ports, &open) {
        Ok(urls) => urls,
        Err(error) => {
            fail_preview(&app, &manager, &project_id, generation, &error, None);
            return None;
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
        return None;
    };
    if let Err(error) = wait_for_http(
        &manager,
        &project_id,
        generation,
        &runtime,
        &open.service,
        &open_url.url,
    ) {
        fail_preview(&app, &manager, &project_id, generation, &error, None);
        return None;
    }
    if let Some(status) = manager.running(&project_id, generation, urls) {
        let _ = app.emit(STATUS_EVENT, status);
        watch_stack(
            app,
            manager,
            project_id,
            generation,
            runtime,
            open.service.clone(),
        );
        // A target inferred from the Compose file is reported back so the
        // stack remembers it.
        run.is_none().then_some(open)
    } else {
        let _ = compose_down(&runtime, false, false);
        remove_source(&runtime.source_root);
        None
    }
}

// The preview runs the exact canonical commit, and a preview check the exact
// commit it checks, fetched as an archive from the internal Forgejo and
// unpacked with the system's tar, so running an app does not need Git or
// Apple's developer tools on the user's machine. The files land in `source`
// under the returned directory.
pub(crate) fn prepare_commit_source(
    repository: &Repository,
    commit: &str,
) -> Result<PathBuf, String> {
    if !safe_commit(commit) {
        return Err("the preview commit is invalid".into());
    }
    let temporary = tempfile::Builder::new()
        .prefix(PREVIEW_TEMP_PREFIX)
        .tempdir()
        .map_err(|e| format!("create preview source directory: {e}"))?;
    let root = temporary.path().to_path_buf();
    let source = root.join("source");
    fs::create_dir_all(&source).map_err(|e| format!("create preview checkout: {e}"))?;
    let archive_url = internal_archive_url(repository, commit)?;
    let token_path = forgejo_token_path()?;
    let token =
        fs::read_to_string(token_path).map_err(|_| "read internal Forgejo token".to_string())?;
    let token = token.trim();
    if token.is_empty() || token.chars().any(char::is_whitespace) {
        return Err("internal Forgejo token is invalid".into());
    }
    let archive = root.join("source.tar.gz");
    // The token goes to curl on stdin, never on its command line.
    let mut download = crate::process::command("curl")
        .args([
            "--fail",
            "--silent",
            "--show-error",
            "--config",
            "-",
            "--output",
        ])
        .arg(&archive)
        .arg(&archive_url)
        .stdin(Stdio::piped())
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|e| format!("fetch canonical preview source: {e}"))?;
    if let Some(mut stdin) = download.stdin.take() {
        writeln!(stdin, "header = \"Authorization: token {token}\"")
            .map_err(|e| format!("fetch canonical preview source: {e}"))?;
    }
    let output = download
        .wait_with_output()
        .map_err(|e| format!("fetch canonical preview source: {e}"))?;
    if !output.status.success() {
        return Err(format!(
            "fetch canonical preview source: {}",
            bounded(&String::from_utf8_lossy(&output.stderr))
        ));
    }
    // Forgejo wraps the files in one top-level folder named after the
    // repository.
    run_checked(
        crate::process::command("tar")
            .arg("-xzf")
            .arg(&archive)
            .arg("-C")
            .arg(&source)
            .arg("--strip-components=1"),
        "unpack canonical preview source",
    )?;
    let _ = fs::remove_file(&archive);
    Ok(temporary.keep())
}

fn internal_archive_url(repository: &Repository, commit: &str) -> Result<String, String> {
    validate_coordinate("repository owner", &repository.owner)?;
    validate_coordinate("repository name", &repository.name)?;
    let port = std::env::var("COMMITARIUM_FORGEJO_PORT").unwrap_or_else(|_| "3001".into());
    Ok(format!(
        "http://127.0.0.1:{port}/api/v1/repos/{}/{}/archive/{commit}.tar.gz",
        repository.owner, repository.name
    ))
}

/// Saves an open target inferred from the Compose file on the project's
/// stack, keeping its tools and services.
async fn remember_open_target(
    client: &reqwest::Client,
    project_id: &str,
    manifest: &ToolchainManifest,
    open: &RunOpen,
) -> Result<(), String> {
    let source = if manifest.source.is_empty() {
        "runtime"
    } else {
        manifest.source.as_str()
    };
    let response = client
        .put(coordinator_url(&format!(
            "/api/v1/projects/{project_id}/toolchain"
        )))
        .json(&serde_json::json!({
            "source": source,
            "tools": manifest.tools,
            "services": manifest.services,
            "run": { "open": open },
        }))
        .send()
        .await
        .map_err(|e| format!("save the preview open target: {e}"))?;
    if !response.status().is_success() {
        return Err(format!(
            "the coordinator refused the open target: HTTP {}",
            response.status()
        ));
    }
    Ok(())
}

pub(crate) fn compose_command(runtime: &ComposeRuntime) -> Command {
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

pub(crate) fn compose_capture(
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

pub(crate) fn spawn_log_reader<R: Read + Send + 'static>(output: R, sender: mpsc::Sender<String>) {
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

pub(crate) fn compose_down_args(volumes: bool, images: bool) -> Vec<&'static str> {
    let mut args = vec!["down", "--remove-orphans"];
    if volumes {
        args.push("-v");
    }
    if images {
        args.extend(["--rmi", "local"]);
    }
    args
}

pub(crate) fn resolve_urls(
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

pub(crate) fn http_responds(port: u16) -> bool {
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
    let services = compose_states(runtime)?;
    Ok(stack_problem(&services, open_service).or_else(|| {
        (!services
            .iter()
            .any(|service| service.service == open_service && service.state == "running"))
        .then(|| format!("the preview open service {open_service:?} stopped"))
    }))
}

/// The containers of a running or starting compose project.
pub(crate) fn compose_states(runtime: &ComposeRuntime) -> Result<Vec<ComposeServiceState>, String> {
    let output = compose_capture(
        runtime,
        &["ps", "--all", "--format", "json"],
        "inspect preview stack",
    )?;
    parse_compose_ps(&output)
}

/// Why a stack is failing, naming the service: one that exited with an
/// error or keeps restarting, or the open service that stopped. A service that finished with exit code 0 (a one-off job)
/// is fine, unless it is the one the preview opens.
pub(crate) fn stack_problem(
    services: &[ComposeServiceState],
    open_service: &str,
) -> Option<String> {
    for service in services {
        let name = &service.service;
        if service.state == "restarting" {
            return Some(format!(
                "the preview service {name:?} keeps restarting (exit code {})",
                service.exit_code
            ));
        }
        if service.exit_code != 0 {
            return Some(format!(
                "the preview service {name:?} exited with code {}",
                service.exit_code
            ));
        }
        if service.state == "dead" {
            return Some(format!("the preview service {name:?} died"));
        }
    }
    services
        .iter()
        .find(|service| service.service == open_service && service.state == "exited")
        .map(|_| format!("the preview open service {open_service:?} stopped"))
}

#[derive(Clone, Debug, Deserialize, PartialEq)]
#[serde(rename_all = "PascalCase")]
pub(crate) struct ComposeServiceState {
    pub(crate) service: String,
    pub(crate) state: String,
    #[serde(default)]
    pub(crate) exit_code: i64,
    /// starting, healthy, or unhealthy; empty without a healthcheck.
    #[serde(default)]
    pub(crate) health: String,
}

/// What each container did, with its last log lines. A service whose logs
/// cannot be read is still listed.
pub(crate) fn collect_evidence(runtime: &ComposeRuntime) -> Vec<ServiceEvidence> {
    let Ok(services) = compose_states(runtime) else {
        return Vec::new();
    };
    let tail = EVIDENCE_LOG_LINES.to_string();
    let mut evidence = Vec::new();
    for state in dedupe_services(services) {
        let logs = compose_capture(
            runtime,
            &[
                "logs",
                "--no-color",
                "--no-log-prefix",
                "--tail",
                &tail,
                &state.service,
            ],
            "read preview service logs",
        )
        .map(|logs| last_lines(&logs, EVIDENCE_LOG_LINES))
        .unwrap_or_default();
        evidence.push(ServiceEvidence {
            service: state.service,
            state: state.state,
            exit_code: state.exit_code,
            health: state.health,
            logs,
        });
    }
    evidence
}

/// One entry per service, a failing container first when a service has
/// several.
pub(crate) fn dedupe_services(services: Vec<ComposeServiceState>) -> Vec<ComposeServiceState> {
    let mut kept: Vec<ComposeServiceState> = Vec::new();
    for service in services {
        match kept.iter_mut().find(|kept| kept.service == service.service) {
            Some(existing) => {
                if existing.exit_code == 0 && service.exit_code != 0 {
                    *existing = service;
                }
            }
            None => kept.push(service),
        }
    }
    kept
}

/// The last `count` lines of `text`, at most 16 KiB, ending in a newline.
pub(crate) fn last_lines(text: &str, count: usize) -> String {
    let lines: Vec<&str> = text.lines().collect();
    let start = lines.len().saturating_sub(count);
    let mut kept = lines[start..].join("\n");
    const LIMIT: usize = 16 * 1024;
    if kept.len() > LIMIT {
        let mut cut = kept.len() - LIMIT;
        while !kept.is_char_boundary(cut) {
            cut += 1;
        }
        kept = kept[cut..].to_string();
    }
    if !kept.is_empty() {
        kept.push('\n');
    }
    kept
}

pub(crate) fn parse_compose_ps(output: &str) -> Result<Vec<ComposeServiceState>, String> {
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
    // Why a container stopped is in its own log, not in the build output.
    let services = manager
        .runtime(project_id, generation)
        .map(|runtime| collect_evidence(&runtime))
        .unwrap_or_default();
    if let Some((status, runtime)) = manager.fail(project_id, generation, &bounded(error), services)
    {
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

pub(crate) fn remove_source(path: &Path) {
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
        // A preview check keeps no data.
        if project.ends_with(crate::preview_check::CHECK_SUFFIX) {
            remove_labeled_resources("volume", &project);
        }
    }
}

pub(crate) fn remove_labeled_resources(kind: &str, project: &str) {
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

pub(crate) async fn coordinator_json<T: for<'de> Deserialize<'de>>(
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

pub(crate) fn coordinator_url(path: &str) -> String {
    let port = std::env::var("COMMITARIUM_COORDINATOR_PORT").unwrap_or_else(|_| "8080".into());
    format!("http://127.0.0.1:{port}{path}")
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

pub(crate) fn validate_handoff(handoff: &ProjectHandoff, project_id: &str) -> Result<(), String> {
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

pub(crate) fn validate_run(run: &RunConfig) -> Result<(), String> {
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

/// Coordinator record IDs such as work orders and checkouts, which may contain
/// uppercase letters.
fn validate_record_id(name: &str, value: &str) -> Result<(), String> {
    if value.is_empty()
        || value.len() > 128
        || !value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || b"_-".contains(&byte))
        || !value.as_bytes()[0].is_ascii_alphanumeric()
    {
        return Err(format!("{name} is invalid"));
    }
    Ok(())
}

pub(crate) fn validate_identifier(name: &str, value: &str) -> Result<(), String> {
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

pub(crate) fn safe_commit(value: &str) -> bool {
    matches!(value.len(), 40 | 64)
        && value
            .bytes()
            .all(|byte| byte.is_ascii_hexdigit() && !byte.is_ascii_uppercase())
}

pub(crate) fn parse_published_port(output: &str) -> Result<u16, String> {
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

pub(crate) fn bounded(value: &str) -> String {
    value.chars().take(4000).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn session_previews_have_their_own_keys_and_compose_projects() {
        assert_eq!(preview_key("prj_abc", None).unwrap(), "prj_abc");
        assert_eq!(
            preview_key("prj_abc", Some("fea_QX2Z")).unwrap(),
            "prj_abc#fea_QX2Z"
        );
        assert!(preview_key("prj_abc", Some("../fea")).is_err());
        let compose_id = session_compose_id("prj_abc", "fea_QX2Z");
        assert_eq!(compose_id, "prj_abc-fea_qx2z");
        assert_eq!(
            preview_compose::project_name(&compose_id).unwrap(),
            "commitarium-preview-prj_abc-fea_qx2z"
        );
    }

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

    fn ps(service: &str, state: &str, exit_code: i64) -> ComposeServiceState {
        ComposeServiceState {
            service: service.into(),
            state: state.into(),
            exit_code,
            health: String::new(),
        }
    }

    #[test]
    fn a_stopped_container_is_named_with_its_exit_code() {
        let running = [ps("web", "running", 0), ps("migrate", "exited", 0)];
        assert_eq!(stack_problem(&running, "web"), None);
        let crashed = [ps("web", "running", 0), ps("api", "exited", 1)];
        assert_eq!(
            stack_problem(&crashed, "web").as_deref(),
            Some("the preview service \"api\" exited with code 1")
        );
        let restarting = [ps("api", "restarting", 2)];
        assert_eq!(
            stack_problem(&restarting, "web").as_deref(),
            Some("the preview service \"api\" keeps restarting (exit code 2)")
        );
        let open_done = [ps("web", "exited", 0)];
        assert_eq!(
            stack_problem(&open_done, "web").as_deref(),
            Some("the preview open service \"web\" stopped")
        );
    }

    #[test]
    fn evidence_keeps_one_entry_per_service_and_the_last_log_lines() {
        let kept = dedupe_services(vec![
            ps("api", "running", 0),
            ps("web", "running", 0),
            ps("api", "exited", 3),
        ]);
        assert_eq!(kept, vec![ps("api", "exited", 3), ps("web", "running", 0)]);
        let logs = (1..=100)
            .map(|n| format!("line {n}"))
            .collect::<Vec<_>>()
            .join("\n");
        let tail = last_lines(&logs, EVIDENCE_LOG_LINES);
        assert!(tail.starts_with("line 41\n") && tail.ends_with("line 100\n"));
        assert_eq!(last_lines("", 60), "");
        assert!(last_lines(&"é".repeat(20_000), 60).len() <= 16 * 1024 + 1);
    }

    #[test]
    fn compose_ps_reads_health_and_the_open_port() {
        let output = r#"{"Service":"db","State":"running","ExitCode":0,"Health":"starting"}"#;
        assert_eq!(parse_compose_ps(output).unwrap()[0].health, "starting");
        let url = PreviewUrl {
            service: "web".into(),
            url: "http://127.0.0.1:49153".into(),
            open: true,
        };
        assert_eq!(url.open_port(), Some(49153));
        assert_eq!(PreviewUrl { open: false, ..url }.open_port(), None);
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
