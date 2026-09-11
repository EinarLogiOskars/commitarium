//! Trusted host-side Docker / Compose launcher.
//!
//! These are the only privileged host operations the renderer can reach, and
//! they are deliberately narrow: every Compose action is scoped to
//! Commitarium's own project name and Compose file. This is not a general
//! Compose or shell runner — the renderer cannot pass an arbitrary file,
//! project, or command through to Docker.

use serde::Serialize;
use std::path::PathBuf;
use std::process::Command;

/// Fixed Compose project name. Scoping every command to this project keeps the
/// launcher from touching any other Compose stack on the host.
const PROJECT_NAME: &str = "commitarium";

/// Official Docker installation page, surfaced to the user when Docker is
/// missing. A fixed, trusted URL — never sourced from runtime data.
const DOCKER_INSTALL_URL: &str = "https://docs.docker.com/get-docker/";

/// Compose profile that adds the real Codex lead/reviewer workers. The launcher
/// enables it so real_codex_lead mode has its workers. (Provider-driven profile
/// selection — e.g. real-claude — can follow once provider config exists.)
const PROFILE: &str = "real-codex";

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
#[derive(Serialize)]
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
fn compose_file() -> Result<PathBuf, String> {
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

/// Run `docker compose [--profile real-codex] -f <file> -p commitarium <args…>`
/// scoped to the Commitarium project and its Compose file — not a general
/// Compose runner. `profiled` enables the real-codex worker profile (for up /
/// update / status); `down` runs unprofiled since it tears down the whole
/// project regardless.
fn compose(profiled: bool, args: &[&str]) -> Result<String, String> {
    let file = compose_file()?;
    let file = file.to_str().ok_or("Compose file path is not valid UTF-8")?;

    let mut command = Command::new("docker");
    command.arg("compose");
    if profiled {
        command.args(["--profile", PROFILE]);
    }
    command.args(["-f", file, "-p", PROJECT_NAME]).args(args);

    let output = command
        .output()
        .map_err(|e| format!("failed to run docker compose: {e}"))?;

    if output.status.success() {
        Ok(String::from_utf8_lossy(&output.stdout).into_owned())
    } else {
        let stderr = String::from_utf8_lossy(&output.stderr);
        Err(format!("docker compose {}: {}", args.join(" "), stderr.trim()))
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

/// Bring the Commitarium stack up in the background, including the real-codex
/// worker profile.
#[tauri::command]
pub fn stack_up() -> Result<(), String> {
    compose(true, &["up", "-d"]).map(|_| ())
}

/// Tear the whole Commitarium stack down (project-wide, all profiles).
#[tauri::command]
pub fn stack_down() -> Result<(), String> {
    compose(false, &["down"]).map(|_| ())
}

/// Update the stack: pull the latest images, then recreate in the background.
#[tauri::command]
pub fn stack_update() -> Result<(), String> {
    compose(true, &["pull"])?;
    compose(true, &["up", "-d"]).map(|_| ())
}

/// Report each Commitarium service's current Compose state.
#[tauri::command]
pub fn stack_status() -> Result<Vec<ServiceStatus>, String> {
    let raw = compose(true, &["ps", "--all", "--format", "json"])?;
    Ok(parse_ps(&raw))
}

/// Parse `docker compose ps --format json` output, which is either one JSON
/// object per line (newer Compose) or a single JSON array (older Compose).
fn parse_ps(raw: &str) -> Vec<ServiceStatus> {
    let mut out = Vec::new();

    let push = |out: &mut Vec<ServiceStatus>, value: &serde_json::Value| {
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
        out.push(ServiceStatus {
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
        });
    };

    let trimmed = raw.trim();
    if trimmed.starts_with('[') {
        if let Ok(serde_json::Value::Array(items)) = serde_json::from_str(trimmed) {
            for item in &items {
                push(&mut out, item);
            }
        }
        return out;
    }

    for line in trimmed.lines().filter(|l| !l.trim().is_empty()) {
        if let Ok(value) = serde_json::from_str::<serde_json::Value>(line) {
            push(&mut out, &value);
        }
    }
    out
}
