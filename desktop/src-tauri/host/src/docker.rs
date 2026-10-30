//! Docker and Compose as the host runs them: every Compose action is scoped to
//! Commitarium's own project name and Compose files.

use serde::Serialize;
use std::collections::BTreeMap;
use std::path::PathBuf;
use std::process::Command;

/// Fixed Compose project name. Scoping every command to this project keeps the
/// launcher from touching any other Compose stack on the host.
pub const PROJECT_NAME: &str = "commitarium";

/// Agent workers are generated without a Compose profile, so lifecycle and
/// status commands address them without one; startup names the exact
/// connected services it is allowed to run. The provider templates they are
/// generated from sit in a profile that is never activated for running.
pub const PROVIDER_PROFILES: &[&str] = &[];

pub const COMPOSE_OVERRIDE_ENV: &str = "COMMITARIUM_COMPOSE_OVERRIDE_FILE";
pub const ENVIRONMENT_COMPOSE_ENV: &str = "COMMITARIUM_ENVIRONMENT_COMPOSE_FILE";
pub const RUNNER_MODE_ENV: &str = "COMMITARIUM_RUNNER_MODE";
/// The generated agents overlay's path, inherited from the desktop.
pub const AGENTS_COMPOSE_ENV: &str = "COMMITARIUM_AGENTS_COMPOSE_FILE";

/// One Commitarium service's current Compose state.
#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct ServiceStatus {
    pub service: String,
    pub state: String,
    pub health: Option<String>,
    pub status: String,
}

/// Resolution order (this is the open "where do the Compose definitions live"
/// decision, implemented pragmatically for development):
///   1. `COMMITARIUM_COMPOSE_FILE` environment override, if set.
///   2. Otherwise walk up from the current directory to the first `compose.yml`
///      (finds the repo root during development).
pub fn compose_file() -> Result<PathBuf, String> {
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
/// The generated agents overlay comes before the approved-environment overlay.
pub fn compose_files(agents_overlay: Option<PathBuf>) -> Result<Vec<PathBuf>, String> {
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
    files.extend(agents_overlay);
    if let Some(path) = std::env::var_os(ENVIRONMENT_COMPOSE_ENV) {
        let path = PathBuf::from(path);
        if path.exists() {
            files.push(path);
        }
    }
    Ok(files)
}

fn append_selected_compose_files(
    command: &mut Command,
    include_environment: bool,
    agents_overlay: Option<PathBuf>,
) -> Result<(), String> {
    let mut files = compose_files(agents_overlay)?;
    if !include_environment {
        if let Some(environment) = std::env::var_os(ENVIRONMENT_COMPOSE_ENV).map(PathBuf::from) {
            files.retain(|path| path != &environment);
        }
    }
    for file in files {
        command.arg("-f").arg(file);
    }
    Ok(())
}

/// Run `docker compose` on the selected definitions. `agents_overlay` is the
/// generated agents overlay, or `None` to leave it out.
pub fn compose_command(
    profiles: &[&str],
    args: &[&str],
    include_environment: bool,
    agents_overlay: Option<PathBuf>,
) -> Result<String, String> {
    let mut command = docker_command();
    command.arg("compose");
    for profile in profiles {
        command.args(["--profile", profile]);
    }
    append_selected_compose_files(&mut command, include_environment, agents_overlay)?;
    command.args(["-p", PROJECT_NAME]).args(args);
    // The desktop drives real agents, in development as in releases; the
    // simulation stays the default only for the stack run without it. An
    // explicit COMMITARIUM_RUNNER_MODE in the environment still wins.
    if std::env::var_os(RUNNER_MODE_ENV).is_none() {
        command.env(RUNNER_MODE_ENV, "real_agents");
    }

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

/// Resolve Docker independently of the GUI process PATH. Apps launched from
/// Finder/Explorer do not inherit an interactive shell PATH, even when Docker
/// Desktop installed its CLI correctly for terminals.
pub fn docker_executable() -> Option<PathBuf> {
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
pub fn docker_command() -> Command {
    let executable = docker_executable().unwrap_or_else(|| PathBuf::from("docker"));
    let mut command = crate::process::command(&executable);

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
pub fn docker_desktop_path() -> Option<PathBuf> {
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
pub fn docker_desktop_path() -> Option<PathBuf> {
    ["ProgramFiles", "LOCALAPPDATA"]
        .into_iter()
        .filter_map(std::env::var_os)
        .map(PathBuf::from)
        .map(|root| root.join("Docker/Docker/Docker Desktop.exe"))
        .find(|path| path.is_file())
}

#[cfg(target_os = "linux")]
pub fn docker_desktop_path() -> Option<PathBuf> {
    [
        "/usr/bin/docker-desktop",
        "/opt/docker-desktop/bin/docker-desktop",
    ]
    .into_iter()
    .map(PathBuf::from)
    .find(|path| path.is_file())
}

#[cfg(not(any(target_os = "macos", target_os = "windows", target_os = "linux")))]
pub fn docker_desktop_path() -> Option<PathBuf> {
    None
}

/// Report each Commitarium service's current Compose state.
pub fn stack_status() -> Result<Vec<ServiceStatus>, String> {
    let overlay = std::env::var_os(AGENTS_COMPOSE_ENV)
        .map(PathBuf::from)
        .filter(|path| path.exists());
    let raw = compose_command(
        PROVIDER_PROFILES,
        &["ps", "--all", "--format", "json"],
        true,
        overlay,
    )?;
    Ok(parse_ps(&raw))
}

/// Parse `docker compose ps --format json` output, which is either one JSON
/// object per line (newer Compose) or a single JSON array (older Compose).
pub fn parse_ps(raw: &str) -> Vec<ServiceStatus> {
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
}
