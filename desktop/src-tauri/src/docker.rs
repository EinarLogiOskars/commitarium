//! Trusted host-side Docker / Compose launcher.
//!
//! These are the only privileged host operations the renderer can reach, and
//! they are deliberately narrow: every Compose action is scoped to
//! Commitarium's own project name and Compose file. This is not a general
//! Compose or shell runner — the renderer cannot pass an arbitrary file,
//! project, or command through to Docker.

use serde::Serialize;
use std::collections::BTreeMap;
use std::path::PathBuf;
use std::process::Command;
use tauri::State;

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

/// Result of probing the host for Docker and Compose readiness.
#[derive(Serialize)]
pub struct DockerProbe {
    docker_installed: bool,
    docker_running: bool,
    compose_available: bool,
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

/// Resolve the Commitarium Compose file.
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

/// Run `docker compose [--profile <fixed profile>...] -f <file> -p
/// commitarium <args…>`
/// scoped to the Commitarium project and its Compose file — not a general
/// Compose runner. Both profile names and service names come only from trusted
/// constants or the fixed provider-profile table, never from the renderer.
fn compose(profiles: &[&str], args: &[&str]) -> Result<String, String> {
    let file = compose_file()?;
    let file = file
        .to_str()
        .ok_or("Compose file path is not valid UTF-8")?;

    let mut command = Command::new("docker");
    command.arg("compose");
    for profile in profiles {
        command.args(["--profile", profile]);
    }
    command.args(["-f", file, "-p", PROJECT_NAME]).args(args);

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

/// Probe the host for Docker and Compose readiness.
#[tauri::command]
pub fn docker_probe() -> DockerProbe {
    let docker_version = probe("docker", &["--version"]);
    // `docker info --format {{.ServerVersion}}` only succeeds when the daemon
    // is actually reachable, so it doubles as the "is the daemon running" check.
    let server_version = probe("docker", &["info", "--format", "{{.ServerVersion}}"]);
    let compose_version = probe("docker", &["compose", "version", "--short"]);

    DockerProbe {
        docker_installed: docker_version.is_some(),
        docker_running: server_version.is_some(),
        compose_available: compose_version.is_some(),
        docker_version,
        compose_version,
        install_url: DOCKER_INSTALL_URL,
    }
}

/// Bring core services up and reconcile every real role worker against its
/// exact provider profile. A disconnected, expired, or failed profile keeps
/// only its matching worker stopped; it does not prevent the app from opening.
#[tauri::command]
pub fn stack_up(manager: State<'_, profiles::ProfileManager>) -> Result<(), String> {
    stack_up_with_manager(manager.inner())
}

fn stack_up_with_manager(manager: &profiles::ProfileManager) -> Result<(), String> {
    let file = compose_file()?;
    let transport_changed = bootstrap::prepare_transport_secrets(&file)?;
    // Forgejo must exist before its own admin CLI can create the internal
    // identities and tokens required by the other services.
    compose(&[], &["up", "-d", "forgejo"])?;
    let forgejo_changed = bootstrap::provision_forgejo(&file, PROJECT_NAME)?;
    let credentials_changed = transport_changed || forgejo_changed;
    start_core_services(credentials_changed)?;
    reconcile_provider_workers(manager, credentials_changed)
}

/// Tear the whole Commitarium stack down (project-wide, all profiles).
#[tauri::command]
pub fn stack_down() -> Result<(), String> {
    compose(PROVIDER_PROFILES, &["down"]).map(|_| ())
}

/// Update the stack: pull the latest images, then recreate in the background.
#[tauri::command]
pub fn stack_update(manager: State<'_, profiles::ProfileManager>) -> Result<(), String> {
    stack_update_with_manager(manager.inner())
}

fn stack_update_with_manager(manager: &profiles::ProfileManager) -> Result<(), String> {
    let file = compose_file()?;
    let transport_changed = bootstrap::prepare_transport_secrets(&file)?;
    compose(PROVIDER_PROFILES, &["pull"])?;
    compose(&[], &["up", "-d", "forgejo"])?;
    let forgejo_changed = bootstrap::provision_forgejo(&file, PROJECT_NAME)?;
    let credentials_changed = transport_changed || forgejo_changed;
    start_core_services(credentials_changed)?;
    reconcile_provider_workers(manager, credentials_changed)
}

fn start_core_services(force_recreate: bool) -> Result<(), String> {
    let mut args = vec!["up", "-d"];
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
        if force_recreate {
            args.push("--force-recreate");
        }
        args.extend(connected);
        compose(PROVIDER_PROFILES, &args)?;
    }
    Ok(())
}

/// Report each Commitarium service's current Compose state.
#[tauri::command]
pub fn stack_status() -> Result<Vec<ServiceStatus>, String> {
    let raw = compose(PROVIDER_PROFILES, &["ps", "--all", "--format", "json"])?;
    Ok(parse_ps(&raw))
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
        let statuses = stack_status().unwrap();

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
