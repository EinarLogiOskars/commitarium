//! Keeping the agents' provider CLIs (Codex, Claude Code) current between
//! image builds. Workers prefer a CLI installed in their shared CLI volume, so
//! an update installs the new version there and restarts the workers. New
//! CLI versions also bring new models.

use std::time::Duration;

use serde::Serialize;

use crate::agents::{self, Provider, CLI_DIRECTORY};
use crate::docker;

const REGISTRY_TIMEOUT: Duration = Duration::from_secs(10);

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CliStatus {
    provider: Provider,
    /// The version the workers run, when one is running.
    installed: Option<String>,
    /// The newest published version, when the registry answered.
    latest: Option<String>,
    update_available: bool,
}

/// The installed and newest version of each provider CLI that has a running
/// worker.
#[tauri::command]
pub async fn check_agent_cli_updates() -> Result<Vec<CliStatus>, String> {
    let mut statuses = Vec::new();
    for provider in [Provider::Codex, Provider::Claude] {
        let installed = tauri::async_runtime::spawn_blocking(move || {
            running_workers(provider)
                .ok()
                .and_then(|containers| containers.first().cloned())
                .and_then(|container| installed_version(&container, provider))
        })
        .await
        .map_err(|_| "the version check stopped unexpectedly".to_string())?;
        let Some(installed) = installed else {
            continue;
        };
        let latest = latest_version(provider).await.ok();
        let update_available = latest
            .as_deref()
            .is_some_and(|latest| newer(latest, &installed));
        statuses.push(CliStatus {
            provider,
            installed: Some(installed),
            latest,
            update_available,
        });
    }
    Ok(statuses)
}

/// Installs `version` of the provider's CLI for its workers and restarts them.
#[tauri::command]
pub async fn update_agent_cli(provider: Provider, version: String) -> Result<(), String> {
    if parse_version(&version).is_none()
        || !version
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b))
    {
        return Err("the CLI version is invalid".into());
    }
    tauri::async_runtime::spawn_blocking(move || {
        let containers = running_workers(provider)?;
        let Some(first) = containers.first() else {
            return Err("no worker for this provider is running".into());
        };
        let install = docker::docker_command()
            .args([
                "exec",
                first,
                "npm",
                "install",
                "--global",
                "--prefix",
                CLI_DIRECTORY,
            ])
            .args([
                "--cache",
                "/tmp/commitarium-npm-cache",
                "--omit=dev",
                "--no-fund",
                "--no-audit",
            ])
            .arg(format!("{}@{version}", provider.package()))
            .output()
            .map_err(|e| format!("install the {} CLI: {e}", provider.executable()))?;
        if !install.status.success() {
            return Err(format!(
                "installing the {} CLI failed: {}",
                provider.executable(),
                String::from_utf8_lossy(&install.stderr).trim()
            ));
        }
        for container in &containers {
            let restart = docker::docker_command()
                .args(["restart", container])
                .output()
                .map_err(|e| format!("restart a worker: {e}"))?;
            if !restart.status.success() {
                return Err("a worker did not restart after the update".into());
            }
        }
        Ok(())
    })
    .await
    .map_err(|_| "the update stopped unexpectedly".to_string())?
}

/// The running worker containers of the provider's agents.
fn running_workers(provider: Provider) -> Result<Vec<String>, String> {
    let mut containers = Vec::new();
    for agent in agents::current()?
        .into_iter()
        .filter(|agent| agent.provider == provider)
    {
        let output = docker::docker_command()
            .args(["ps", "--quiet", "--filter"])
            .arg(format!(
                "label=com.docker.compose.project={}",
                docker::PROJECT_NAME
            ))
            .arg("--filter")
            .arg(format!(
                "label=com.docker.compose.service={}",
                agents::worker_service(&agent.id)
            ))
            .output()
            .map_err(|e| format!("find the agent workers: {e}"))?;
        containers.extend(
            String::from_utf8_lossy(&output.stdout)
                .lines()
                .map(str::trim)
                .filter(|id| !id.is_empty())
                .map(str::to_string),
        );
    }
    Ok(containers)
}

/// The CLI version a worker runs: an updated one when installed, else the
/// image's.
fn installed_version(container: &str, provider: Provider) -> Option<String> {
    let executable = provider.executable();
    let script = format!(
        "if [ -x {CLI_DIRECTORY}/bin/{executable} ]; then {CLI_DIRECTORY}/bin/{executable} --version; else {executable} --version; fi"
    );
    let output = docker::docker_command()
        .args(["exec", container, "sh", "-c", &script])
        .output()
        .ok()?;
    if !output.status.success() {
        return None;
    }
    first_version(&String::from_utf8_lossy(&output.stdout))
}

async fn latest_version(provider: Provider) -> Result<String, String> {
    #[derive(serde::Deserialize)]
    struct Latest {
        version: String,
    }
    let url = format!(
        "https://registry.npmjs.org/{}/latest",
        provider.package().replace('/', "%2f")
    );
    let latest: Latest = reqwest::Client::builder()
        .timeout(REGISTRY_TIMEOUT)
        .build()
        .map_err(|e| e.to_string())?
        .get(url)
        .send()
        .await
        .map_err(|e| format!("ask npm for the newest CLI: {e}"))?
        .json()
        .await
        .map_err(|e| format!("read npm's answer: {e}"))?;
    parse_version(&latest.version)
        .map(|_| latest.version)
        .ok_or_else(|| "npm answered an invalid version".into())
}

/// The first `x.y.z` version in a CLI's `--version` output.
fn first_version(output: &str) -> Option<String> {
    output
        .split_whitespace()
        .map(|word| word.trim_start_matches('v'))
        .find(|word| parse_version(word).is_some())
        .map(str::to_string)
}

fn parse_version(value: &str) -> Option<(u64, u64, u64)> {
    let core = value.split(['-', '+']).next()?;
    let mut parts = core.split('.').map(|part| part.parse::<u64>().ok());
    let version = (parts.next()??, parts.next()??, parts.next()??);
    parts.next().is_none().then_some(version)
}

/// Whether `candidate` is a newer release than `current`.
fn newer(candidate: &str, current: &str) -> bool {
    match (parse_version(candidate), parse_version(current)) {
        (Some(candidate), Some(current)) => candidate > current,
        _ => false,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn versions_are_read_and_compared() {
        assert_eq!(
            first_version("2.1.270 (Claude Code)").as_deref(),
            Some("2.1.270")
        );
        assert_eq!(
            first_version("codex-cli 0.153.4").as_deref(),
            Some("0.153.4")
        );
        assert_eq!(first_version("unknown"), None);
        assert!(newer("0.160.0", "0.153.4"));
        assert!(newer("2.10.0", "2.9.9"));
        assert!(!newer("0.153.4", "0.153.4"));
        assert!(!newer("garbage", "0.1.0"));
    }
}
