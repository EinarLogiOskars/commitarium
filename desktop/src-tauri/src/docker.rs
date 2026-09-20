//! Trusted host-side Docker / Compose launcher.
//!
//! These are the only privileged host operations the renderer can reach, and
//! they are deliberately narrow: every Compose action is scoped to
//! Commitarium's own project name and Compose file. This is not a general
//! Compose or shell runner — the renderer cannot pass an arbitrary file,
//! project, or command through to Docker.

use serde::Serialize;
use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use tauri::{AppHandle, Manager, State};

use crate::{bootstrap, profiles};

/// Fixed Compose project name. Scoping every command to this project keeps the
/// launcher from touching any other Compose stack on the host.
pub(crate) const PROJECT_NAME: &str = "commitarium";

/// Official Docker installation page, surfaced to the user when Docker is
/// missing. A fixed, trusted URL — never sourced from runtime data.
const DOCKER_INSTALL_URL: &str = "https://docs.docker.com/get-docker/";

/// Every opt-in provider profile. Lifecycle and status commands include both
/// so they can address any previously created role worker, while startup names
/// the exact connected services it is allowed to run.
const PROVIDER_PROFILES: &[&str] = &["real-codex", "real-claude"];

/// Services that do not contain provider credentials and are useful even when
/// no real agent profile has been connected yet.
const CORE_SERVICES: &[&str] = &["coordinator", "simulated-codex-worker"];

const COMPOSE_OVERRIDE_ENV: &str = "COMMITARIUM_COMPOSE_OVERRIDE_FILE";
const RELEASE_MODE_ENV: &str = "COMMITARIUM_RELEASE_MODE";
const EMBEDDED_COMPOSE: &str = include_str!("../../../compose.yml");
const EMBEDDED_RELEASE_COMPOSE: &str = include_str!("../../../compose.release.yml");

/// Result of probing the host for Docker and Compose readiness.
#[derive(Serialize)]
pub struct DockerProbe {
    docker_installed: bool,
    docker_running: bool,
    compose_available: bool,
    docker_launchable: bool,
    docker_version: Option<String>,
    compose_version: Option<String>,
    install_url: &'static str,
}

/// One Commitarium service's current Compose state.
#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct ServiceStatus {
    service: String,
    state: String,
    health: Option<String>,
    status: String,
}

/// Install the release Compose definitions into the per-user data directory.
/// Development builds keep resolving the repository Compose file, and an
/// explicit environment override always wins for tests and advanced use.
pub(crate) fn prepare_runtime(app: &AppHandle) -> Result<(), String> {
    if std::env::var_os("COMMITARIUM_COMPOSE_FILE").is_some() || cfg!(debug_assertions) {
        return Ok(());
    }

    let runtime_dir = app
        .path()
        .app_data_dir()
        .map_err(|e| format!("resolve app data directory: {e}"))?
        .join("runtime");
    let (compose_file, release_file) = materialize_release_compose(&runtime_dir)?;

    std::env::set_var("COMMITARIUM_COMPOSE_FILE", compose_file);
    std::env::set_var(COMPOSE_OVERRIDE_ENV, release_file);
    std::env::set_var("COMMITARIUM_IMAGE_TAG", env!("CARGO_PKG_VERSION"));
    std::env::set_var(RELEASE_MODE_ENV, "1");
    Ok(())
}

fn materialize_release_compose(runtime_dir: &Path) -> Result<(PathBuf, PathBuf), String> {
    fs::create_dir_all(runtime_dir).map_err(|e| format!("create Docker runtime directory: {e}"))?;
    let compose_file = runtime_dir.join("compose.yml");
    let release_file = runtime_dir.join("compose.release.yml");
    fs::write(&compose_file, EMBEDDED_COMPOSE)
        .map_err(|e| format!("write embedded Compose definition: {e}"))?;
    fs::write(&release_file, EMBEDDED_RELEASE_COMPOSE)
        .map_err(|e| format!("write embedded release Compose definition: {e}"))?;
    Ok((compose_file, release_file))
}

/// Resolve the primary Commitarium Compose file.
///
/// Resolution order (this is the open "where do the Compose definitions live"
/// decision, implemented pragmatically for development):
///   1. `COMMITARIUM_COMPOSE_FILE` environment override, if set.
///   2. Otherwise walk up from the current directory to the first `compose.yml`
///      (finds the repo root during development).
pub(crate) fn compose_file() -> Result<PathBuf, String> {
    if let Ok(path) = std::env::var("COMMITARIUM_COMPOSE_FILE") {
        let path = PathBuf::from(path);
        if path.exists() {
            return Ok(path);
        }
        return Err(format!(
            "COMMITARIUM_COMPOSE_FILE points at a missing file: {}",
            path.display()
        ));
    }

    let mut dir = std::env::current_dir().map_err(|e| e.to_string())?;
    loop {
        let candidate = dir.join("compose.yml");
        if candidate.exists() {
            return Ok(candidate);
        }
        if !dir.pop() {
            return Err(
                "compose.yml not found; set COMMITARIUM_COMPOSE_FILE to its path".to_string(),
            );
        }
    }
}

/// Resolve the complete Compose file set. Installed releases add the embedded
/// release overlay, while development and tests normally use only compose.yml.
pub(crate) fn compose_files() -> Result<Vec<PathBuf>, String> {
    let mut files = vec![compose_file()?];
    if let Some(path) = std::env::var_os(COMPOSE_OVERRIDE_ENV) {
        let path = PathBuf::from(path);
        if !path.exists() {
            return Err(format!(
                "{COMPOSE_OVERRIDE_ENV} points at a missing file: {}",
                path.display()
            ));
        }
        files.push(path);
    }
    Ok(files)
}

pub(crate) fn append_compose_files(command: &mut Command) -> Result<(), String> {
    for file in compose_files()? {
        command.arg("-f").arg(file);
    }
    Ok(())
}

pub(crate) fn release_mode() -> bool {
    std::env::var(RELEASE_MODE_ENV).as_deref() == Ok("1")
}

/// Run `docker compose [--profile <fixed profile>...] -f <file> -p
/// commitarium <args…>`
/// scoped to the Commitarium project and its Compose file — not a general
/// Compose runner. Both profile names and service names come only from trusted
/// constants or the fixed provider-profile table, never from the renderer.
fn compose(profiles: &[&str], args: &[&str]) -> Result<String, String> {
    let mut command = docker_command();
    command.arg("compose");
    for profile in profiles {
        command.args(["--profile", profile]);
    }
    append_compose_files(&mut command)?;
    command.args(["-p", PROJECT_NAME]).args(args);

    let output = command
        .output()
        .map_err(|e| format!("failed to run docker compose: {e}"))?;

    if output.status.success() {
        Ok(String::from_utf8_lossy(&output.stdout).into_owned())
    } else {
        let stderr = String::from_utf8_lossy(&output.stderr);
        Err(format!(
            "docker compose {}: {}",
            args.join(" "),
            stderr.trim()
        ))
    }
}

/// Run a plain command and return its trimmed stdout when it exits zero.
fn probe(program: &str, args: &[&str]) -> Option<String> {
    let output = Command::new(program).args(args).output().ok()?;
    if output.status.success() {
        Some(String::from_utf8_lossy(&output.stdout).trim().to_string())
    } else {
        None
    }
}

/// Resolve Docker independently of the GUI process PATH. Apps launched from
/// Finder/Explorer do not inherit an interactive shell PATH, even when Docker
/// Desktop installed its CLI correctly for terminals.
fn docker_executable() -> Option<PathBuf> {
    let executable_name = if cfg!(windows) {
        "docker.exe"
    } else {
        "docker"
    };

    if let Some(path) = std::env::var_os("PATH") {
        for directory in std::env::split_paths(&path) {
            let candidate = directory.join(executable_name);
            if candidate.is_file() {
                return Some(candidate);
            }
        }
    }

    #[cfg(target_os = "macos")]
    {
        for candidate in [
            "/usr/local/bin/docker",
            "/opt/homebrew/bin/docker",
            "/Applications/Docker.app/Contents/Resources/bin/docker",
        ] {
            let candidate = PathBuf::from(candidate);
            if candidate.is_file() {
                return Some(candidate);
            }
        }
        if let Some(candidate) = docker_desktop_path()
            .map(|app| app.join("Contents/Resources/bin/docker"))
            .filter(|path| path.is_file())
        {
            return Some(candidate);
        }
    }

    #[cfg(target_os = "windows")]
    {
        if let Some(program_files) = std::env::var_os("ProgramFiles") {
            let candidate =
                PathBuf::from(program_files).join("Docker/Docker/resources/bin/docker.exe");
            if candidate.is_file() {
                return Some(candidate);
            }
        }
    }

    #[cfg(target_os = "linux")]
    {
        for candidate in [
            "/usr/bin/docker",
            "/usr/local/bin/docker",
            "/snap/bin/docker",
        ] {
            let candidate = PathBuf::from(candidate);
            if candidate.is_file() {
                return Some(candidate);
            }
        }
    }

    None
}

/// Create a Docker CLI command using the same host-side resolution everywhere
/// in the desktop backend, including profile login and Forgejo bootstrap.
pub(crate) fn docker_command() -> Command {
    let executable = docker_executable().unwrap_or_else(|| PathBuf::from("docker"));
    let mut command = Command::new(&executable);

    // Docker invokes credential helpers (for example
    // `docker-credential-osxkeychain`) by name. A packaged app's PATH commonly
    // omits both /usr/local/bin and Docker Desktop's bundled bin directory, so
    // resolving the Docker binary alone is not enough for authenticated pulls.
    let mut paths = Vec::new();
    if let Some(parent) = executable
        .parent()
        .filter(|path| !path.as_os_str().is_empty())
    {
        paths.push(parent.to_path_buf());
    }
    #[cfg(target_os = "macos")]
    if let Some(bundled_bin) = docker_desktop_path()
        .map(|app| app.join("Contents/Resources/bin"))
        .filter(|path| path.is_dir())
    {
        if !paths.contains(&bundled_bin) {
            paths.push(bundled_bin);
        }
    }
    if let Some(current) = std::env::var_os("PATH") {
        for path in std::env::split_paths(&current) {
            if !paths.contains(&path) {
                paths.push(path);
            }
        }
    }
    if let Ok(path) = std::env::join_paths(paths) {
        command.env("PATH", path);
    }

    command
}

#[cfg(target_os = "macos")]
fn docker_desktop_path() -> Option<PathBuf> {
    let system = PathBuf::from("/Applications/Docker.app");
    if system.is_dir() {
        return Some(system);
    }
    std::env::var_os("HOME")
        .map(PathBuf::from)
        .map(|home| home.join("Applications/Docker.app"))
        .filter(|path| path.is_dir())
}

#[cfg(target_os = "windows")]
fn docker_desktop_path() -> Option<PathBuf> {
    ["ProgramFiles", "LOCALAPPDATA"]
        .into_iter()
        .filter_map(std::env::var_os)
        .map(PathBuf::from)
        .map(|root| root.join("Docker/Docker/Docker Desktop.exe"))
        .find(|path| path.is_file())
}

#[cfg(target_os = "linux")]
fn docker_desktop_path() -> Option<PathBuf> {
    [
        "/usr/bin/docker-desktop",
        "/opt/docker-desktop/bin/docker-desktop",
    ]
    .into_iter()
    .map(PathBuf::from)
    .find(|path| path.is_file())
}

#[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
fn docker_desktop_path() -> Option<PathBuf> {
    None
}

fn probe_docker(args: &[&str]) -> Option<String> {
    let executable = docker_executable()?;
    probe(executable.to_string_lossy().as_ref(), args)
}

fn docker_probe_blocking() -> DockerProbe {
    let executable_found = docker_executable().is_some();
    let docker_version = probe_docker(&["--version"]);
    // `docker info --format {{.ServerVersion}}` only succeeds when the daemon
    // is actually reachable, so it doubles as the "is the daemon running" check.
    let server_version = probe_docker(&["info", "--format", "{{.ServerVersion}}"]);
    let compose_version = probe_docker(&["compose", "version", "--short"]);
    let desktop_path = docker_desktop_path();

    DockerProbe {
        docker_installed: executable_found || desktop_path.is_some(),
        docker_running: server_version.is_some(),
        compose_available: compose_version.is_some(),
        docker_launchable: desktop_path.is_some(),
        docker_version,
        compose_version,
        install_url: DOCKER_INSTALL_URL,
    }
}

/// Probe the host for Docker and Compose readiness without blocking the app's
/// event loop while Docker Desktop is still starting.
#[tauri::command]
pub async fn docker_probe() -> Result<DockerProbe, String> {
    tauri::async_runtime::spawn_blocking(docker_probe_blocking)
        .await
        .map_err(|_| "could not inspect Docker Desktop".to_string())
}

/// Launch only a detected Docker Desktop installation. The renderer cannot
/// supply a program or arguments, preserving the native command boundary.
#[tauri::command]
pub fn launch_docker_desktop() -> Result<(), String> {
    let path = docker_desktop_path()
        .ok_or_else(|| "Docker Desktop is not installed in a supported location".to_string())?;

    #[cfg(target_os = "macos")]
    let mut command = {
        let mut command = Command::new("/usr/bin/open");
        command.arg(path);
        command
    };

    #[cfg(any(target_os = "windows", target_os = "linux"))]
    let mut command = Command::new(path);

    #[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
    let mut command = Command::new(path);

    command
        .spawn()
        .map(|_| ())
        .map_err(|e| format!("could not launch Docker Desktop: {e}"))
}

/// Bring core services up and reconcile every real role worker against its
/// exact provider profile. A disconnected, expired, or failed profile keeps
/// only its matching worker stopped; it does not prevent the app from opening.
#[tauri::command]
pub async fn stack_up(manager: State<'_, profiles::ProfileManager>) -> Result<(), String> {
    let manager = manager.inner().clone();
    tauri::async_runtime::spawn_blocking(move || stack_up_with_manager(&manager))
        .await
        .map_err(|_| "could not start the Commitarium stack".to_string())?
}

fn stack_up_with_manager(manager: &profiles::ProfileManager) -> Result<(), String> {
    let file = compose_file()?;
    let transport_changed = bootstrap::prepare_transport_secrets(&file)?;
    if release_mode() {
        compose(PROVIDER_PROFILES, &["pull", "--policy", "missing"])?;
    }
    // Forgejo must exist before its own admin CLI can create the internal
    // identities and tokens required by the other services.
    start_forgejo()?;
    let forgejo_changed = bootstrap::provision_forgejo(&file, PROJECT_NAME)?;
    let credentials_changed = transport_changed || forgejo_changed;
    start_core_services(credentials_changed)?;
    reconcile_provider_workers(manager, credentials_changed)
}

/// Tear the whole Commitarium stack down (project-wide, all profiles).
#[tauri::command]
pub async fn stack_down() -> Result<(), String> {
    tauri::async_runtime::spawn_blocking(|| compose(PROVIDER_PROFILES, &["down"]).map(|_| ()))
        .await
        .map_err(|_| "could not stop the Commitarium stack".to_string())?
}

/// Update the stack: pull the latest images, then recreate in the background.
#[tauri::command]
pub async fn stack_update(manager: State<'_, profiles::ProfileManager>) -> Result<(), String> {
    let manager = manager.inner().clone();
    tauri::async_runtime::spawn_blocking(move || stack_update_with_manager(&manager))
        .await
        .map_err(|_| "could not update the Commitarium stack".to_string())?
}

fn stack_update_with_manager(manager: &profiles::ProfileManager) -> Result<(), String> {
    let file = compose_file()?;
    let transport_changed = bootstrap::prepare_transport_secrets(&file)?;
    compose(PROVIDER_PROFILES, &["pull"])?;
    start_forgejo()?;
    let forgejo_changed = bootstrap::provision_forgejo(&file, PROJECT_NAME)?;
    let credentials_changed = transport_changed || forgejo_changed;
    start_core_services(credentials_changed)?;
    reconcile_provider_workers(manager, credentials_changed)
}

fn start_forgejo() -> Result<(), String> {
    let mut args = vec!["up", "-d"];
    if release_mode() {
        args.push("--no-build");
    }
    args.push("forgejo");
    compose(&[], &args).map(|_| ())
}

fn start_core_services(force_recreate: bool) -> Result<(), String> {
    let mut args = vec!["up", "-d"];
    if release_mode() {
        args.push("--no-build");
    }
    if force_recreate {
        args.push("--force-recreate");
    }
    args.extend_from_slice(CORE_SERVICES);
    compose(&[], &args).map(|_| ())
}

fn reconcile_provider_workers(
    manager: &profiles::ProfileManager,
    force_recreate: bool,
) -> Result<(), String> {
    let connections = profiles::worker_connections(manager);
    let stopped: Vec<_> = connections
        .iter()
        .filter(|connection| !connection.connected)
        .map(|connection| connection.service)
        .collect();
    if !stopped.is_empty() {
        let mut args = vec!["stop"];
        args.extend(stopped.iter().copied());
        compose(PROVIDER_PROFILES, &args)?;
        if force_recreate {
            let mut args = vec!["create", "--force-recreate"];
            if release_mode() {
                args.push("--no-build");
            }
            args.extend(stopped);
            compose(PROVIDER_PROFILES, &args)?;
        }
    }

    let connected: Vec<_> = connections
        .iter()
        .filter(|connection| connection.connected)
        .map(|connection| connection.service)
        .collect();
    if !connected.is_empty() {
        let mut args = vec!["up", "-d"];
        if release_mode() {
            args.push("--no-build");
        }
        if force_recreate {
            args.push("--force-recreate");
        }
        args.extend(connected);
        compose(PROVIDER_PROFILES, &args)?;
    }
    Ok(())
}

/// Report each Commitarium service's current Compose state.
fn stack_status_blocking() -> Result<Vec<ServiceStatus>, String> {
    let raw = compose(PROVIDER_PROFILES, &["ps", "--all", "--format", "json"])?;
    Ok(parse_ps(&raw))
}

#[tauri::command]
pub async fn stack_status() -> Result<Vec<ServiceStatus>, String> {
    tauri::async_runtime::spawn_blocking(stack_status_blocking)
        .await
        .map_err(|_| "could not inspect the Commitarium stack".to_string())?
}

/// Parse `docker compose ps --format json` output, which is either one JSON
/// object per line (newer Compose) or a single JSON array (older Compose).
fn parse_ps(raw: &str) -> Vec<ServiceStatus> {
    let mut out = BTreeMap::new();

    let push = |out: &mut BTreeMap<String, ServiceStatus>, value: &serde_json::Value| {
        let service = value
            .get("Service")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
        if service.is_empty() {
            return;
        }
        let health = value
            .get("Health")
            .and_then(|v| v.as_str())
            .filter(|s| !s.is_empty())
            .map(|s| s.to_string());
        let candidate = ServiceStatus {
            service,
            state: value
                .get("State")
                .and_then(|v| v.as_str())
                .unwrap_or("unknown")
                .to_string(),
            health,
            status: value
                .get("Status")
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .to_string(),
        };
        match out.get(&candidate.service) {
            Some(existing) if service_status_rank(existing) >= service_status_rank(&candidate) => {}
            _ => {
                out.insert(candidate.service.clone(), candidate);
            }
        }
    };

    let trimmed = raw.trim();
    if trimmed.starts_with('[') {
        if let Ok(serde_json::Value::Array(items)) = serde_json::from_str(trimmed) {
            for item in &items {
                push(&mut out, item);
            }
        }
        return out.into_values().collect();
    }

    for line in trimmed.lines().filter(|l| !l.trim().is_empty()) {
        if let Ok(value) = serde_json::from_str::<serde_json::Value>(line) {
            push(&mut out, &value);
        }
    }
    out.into_values().collect()
}

/// Compose can briefly report both a replaced container and its successor for
/// one service. Prefer the row that best represents an available service, then
/// return services in stable name order so repeated UI refreshes do not flicker.
fn service_status_rank(status: &ServiceStatus) -> (u8, u8) {
    let state = match status.state.as_str() {
        "running" => 6,
        "restarting" => 5,
        "paused" => 4,
        "created" => 3,
        "exited" => 2,
        "dead" => 1,
        _ => 0,
    };
    let health = match status.health.as_deref() {
        Some("healthy") => 2,
        Some("starting") => 1,
        _ => 0,
    };
    (state, health)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[cfg(target_os = "macos")]
    #[test]
    fn docker_command_exposes_desktop_credential_helpers() {
        let Some(app) = docker_desktop_path() else {
            return;
        };
        let bundled_bin = app.join("Contents/Resources/bin");
        let command = docker_command();
        let configured_path = command
            .get_envs()
            .find(|(name, _)| *name == std::ffi::OsStr::new("PATH"))
            .and_then(|(_, value)| value)
            .expect("Docker command should carry an explicit PATH");

        assert!(std::env::split_paths(configured_path).any(|path| path == bundled_bin));
        assert!(bundled_bin.join("docker-credential-osxkeychain").is_file());
    }

    #[test]
    fn materializes_embedded_release_compose_files() {
        let root = tempfile::tempdir().unwrap();
        let (base, release) = materialize_release_compose(root.path()).unwrap();

        assert_eq!(fs::read_to_string(base).unwrap(), EMBEDDED_COMPOSE);
        assert_eq!(
            fs::read_to_string(release).unwrap(),
            EMBEDDED_RELEASE_COMPOSE
        );
        assert!(EMBEDDED_RELEASE_COMPOSE.contains("build: !reset null"));
        assert!(EMBEDDED_RELEASE_COMPOSE.contains("ghcr.io/einarlogioskars"));
        assert!(EMBEDDED_RELEASE_COMPOSE.contains("COMMITARIUM_RUNNER_MODE: real_agents"));
        assert_eq!(
            EMBEDDED_COMPOSE.matches("seccomp=unconfined").count(),
            2,
            "both Codex workers must allow the nested read-only bwrap sandbox"
        );
    }

    #[test]
    fn compose_json_lines_are_sorted_and_deduplicated_by_service() {
        let raw = r#"{"Service":"forgejo","State":"created","Health":"","Status":"Created"}
{"Service":"coordinator","State":"running","Health":"","Status":"Up"}
{"Service":"forgejo","State":"running","Health":"","Status":"Up"}"#;

        let statuses = parse_ps(raw);

        assert_eq!(statuses.len(), 2);
        assert_eq!(statuses[0].service, "coordinator");
        assert_eq!(statuses[1].service, "forgejo");
        assert_eq!(statuses[1].state, "running");
    }

    #[test]
    fn compose_json_array_prefers_healthy_running_row() {
        let raw = r#"[
          {"Service":"codex-worker","State":"running","Health":"starting","Status":"Up"},
          {"Service":"codex-worker","State":"running","Health":"healthy","Status":"Up (healthy)"}
        ]"#;

        let statuses = parse_ps(raw);

        assert_eq!(statuses.len(), 1);
        assert_eq!(statuses[0].health.as_deref(), Some("healthy"));
        assert_eq!(statuses[0].status, "Up (healthy)");
    }

    /// Exercises the same internal entry point as the Tauri command against a
    /// real development stack. It is opt-in because it starts/reconciles
    /// containers and requires Docker plus the provider images.
    #[test]
    #[ignore = "requires and mutates the local Commitarium Docker stack"]
    fn live_start_matches_each_provider_connection() {
        let manager = profiles::ProfileManager::new();
        let expected = profiles::worker_connections(&manager);

        stack_up_with_manager(&manager).unwrap();
        let statuses = stack_status_blocking().unwrap();

        for core in ["forgejo", "coordinator", "simulated-codex-worker"] {
            assert!(statuses
                .iter()
                .any(|status| status.service == core && status.state == "running"));
        }
        for connection in expected {
            let running = statuses
                .iter()
                .any(|status| status.service == connection.service && status.state == "running");
            assert_eq!(running, connection.connected, "{}", connection.service);
        }
    }
}
