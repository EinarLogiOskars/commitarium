//! Keeping the agents' provider CLIs (Codex, Claude Code) current between
//! image builds. They are Commitarium's own tooling, so they always run the
//! newest release: a background task checks at launch and every few hours,
//! installs a newer CLI into the provider's shared CLI volume (the workers
//! prefer it), and restarts the provider's workers once none of them is
//! running a turn. New CLI versions also bring new models.

use std::sync::Mutex;
use std::thread;
use std::time::{Duration, Instant};

use serde::Serialize;
use tauri::{AppHandle, Emitter, Manager};
use tokio::sync::Notify;

use crate::agents::{self, Provider, CLI_DIRECTORY};
use crate::docker;

const REGISTRY_TIMEOUT: Duration = Duration::from_secs(10);
/// How often the CLIs are checked once they are current.
const CHECK_INTERVAL: Duration = Duration::from_secs(6 * 60 * 60);
/// How soon a pass is retried while an update waits for running work, failed,
/// or no worker was up to check.
const RETRY_INTERVAL: Duration = Duration::from_secs(3 * 60);
const HEALTHY_TIMEOUT: Duration = Duration::from_secs(120);
const HEALTH_POLL: Duration = Duration::from_secs(1);
pub(crate) const STATUS_EVENT: &str = "agent_cli_status";
const PROVIDERS: [Provider; 2] = [Provider::Codex, Provider::Claude];

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum CliState {
    /// The workers run the newest release.
    Current,
    /// Installing `target` or restarting the workers onto it.
    Updating,
    /// `target` is installed; restarting the workers waits until none of
    /// them is running a turn.
    Waiting,
    /// `target` could not be installed or restarted onto; the next pass
    /// tries again.
    Behind,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct CliStatus {
    provider: Provider,
    /// The version installed for the workers.
    installed: String,
    /// The version being updated to, unless the CLI is current.
    target: Option<String>,
    state: CliState,
}

/// The updater's shared state. A single background task does every update,
/// so two never overlap.
#[derive(Default)]
pub(crate) struct CliUpdater {
    statuses: Mutex<Vec<CliStatus>>,
    /// Versions installed whose workers still wait for a restart.
    restart_pending: Mutex<Vec<(Provider, String)>>,
    wake: Notify,
}

/// The agents' CLI versions and any update in progress.
#[tauri::command]
pub fn agent_cli_status(updater: tauri::State<'_, CliUpdater>) -> Vec<CliStatus> {
    updater
        .statuses
        .lock()
        .map(|s| s.clone())
        .unwrap_or_default()
}

/// Check now, for example because the stack (and so the workers) just came up.
pub(crate) fn wake(app: &AppHandle) {
    app.state::<CliUpdater>().wake.notify_one();
}

/// Start the background updater. It never blocks startup; a failed pass is
/// logged and retried.
pub(crate) fn spawn(app: AppHandle) {
    tauri::async_runtime::spawn(async move {
        loop {
            let delay = match update_pass(&app).await {
                Ok(true) => CHECK_INTERVAL,
                Ok(false) => RETRY_INTERVAL,
                Err(error) => {
                    eprintln!("[cli-updates] {error}");
                    RETRY_INTERVAL
                }
            };
            let updater = app.state::<CliUpdater>();
            let _ = tokio::time::timeout(delay, updater.wake.notified()).await;
        }
    });
}

/// One pass over both providers. Returns whether everything is settled: each
/// provider with a running worker is current and at least one was checked.
async fn update_pass(app: &AppHandle) -> Result<bool, String> {
    let mut settled = true;
    let mut checked = false;
    for provider in PROVIDERS {
        let containers = blocking(move || running_workers(provider)).await??;
        let Some(first) = containers.first().cloned() else {
            set_status(app, provider, None);
            continue;
        };
        let Some(installed) = blocking({
            let first = first.clone();
            move || installed_version(&first, provider)
        })
        .await?
        else {
            settled = false;
            continue;
        };
        checked = true;
        let latest = match latest_version(provider).await {
            Ok(latest) => Some(latest),
            Err(error) => {
                eprintln!("[cli-updates] {error}");
                settled = false;
                None
            }
        };
        let pending = pending_restart(app, provider);
        let target = match plan(&installed, latest.as_deref(), pending.as_deref()) {
            Plan::Nothing => {
                set_pending_restart(app, provider, None);
                set_status(app, provider, Some(current(provider, &installed)));
                continue;
            }
            Plan::Install(version) | Plan::Restart(version) => version,
        };
        let status = |state| CliStatus {
            provider,
            installed: installed.clone(),
            target: Some(target.clone()),
            state,
        };
        if pending.as_deref() != Some(target.as_str()) {
            set_status(app, provider, Some(status(CliState::Updating)));
            let result = blocking({
                let (first, version) = (first.clone(), target.clone());
                move || install(&first, provider, &version)
            })
            .await?;
            if let Err(error) = result {
                eprintln!("[cli-updates] {error}");
                set_status(app, provider, Some(status(CliState::Behind)));
                settled = false;
                continue;
            }
            set_pending_restart(app, provider, Some(target.clone()));
        }
        // Installing left running turns alone; restarting would end them.
        let idle = blocking({
            let containers = containers.clone();
            move || workers_idle(&containers)
        })
        .await?;
        if !idle {
            set_status(app, provider, Some(status(CliState::Waiting)));
            settled = false;
            continue;
        }
        set_status(app, provider, Some(status(CliState::Updating)));
        let restarted = blocking(move || {
            restart(&containers)?;
            wait_until_healthy(&containers)
        })
        .await?;
        if let Err(error) = restarted {
            eprintln!("[cli-updates] {error}");
            set_status(app, provider, Some(status(CliState::Behind)));
            settled = false;
            continue;
        }
        set_pending_restart(app, provider, None);
        set_status(app, provider, Some(current(provider, &target)));
    }
    Ok(settled && checked)
}

fn current(provider: Provider, installed: &str) -> CliStatus {
    CliStatus {
        provider,
        installed: installed.to_string(),
        target: None,
        state: CliState::Current,
    }
}

/// What a pass does for one provider.
#[derive(Debug, Eq, PartialEq)]
enum Plan {
    Nothing,
    /// Install this version, then restart the workers onto it.
    Install(String),
    /// This version is installed; restart the workers onto it.
    Restart(String),
}

/// `installed` is the version in the CLI volume (or the image's), so after an
/// install it already reads the new version while the workers still wait for
/// the restart recorded in `pending`. Without the registry (offline) a
/// pending restart still runs.
fn plan(installed: &str, latest: Option<&str>, pending: Option<&str>) -> Plan {
    match latest {
        Some(latest) if newer(latest, installed) => Plan::Install(latest.to_string()),
        _ => match pending {
            Some(pending) if !newer(installed, pending) => Plan::Restart(pending.to_string()),
            _ => Plan::Nothing,
        },
    }
}

fn set_status(app: &AppHandle, provider: Provider, status: Option<CliStatus>) {
    let updater = app.state::<CliUpdater>();
    let Ok(mut statuses) = updater.statuses.lock() else {
        return;
    };
    let before = serde_json::to_value(&*statuses).ok();
    statuses.retain(|s| s.provider != provider);
    statuses.extend(status);
    statuses.sort_by_key(|s| PROVIDERS.iter().position(|p| *p == s.provider));
    if serde_json::to_value(&*statuses).ok() != before {
        let _ = app.emit(STATUS_EVENT, statuses.clone());
    }
}

fn pending_restart(app: &AppHandle, provider: Provider) -> Option<String> {
    let updater = app.state::<CliUpdater>();
    let pending = updater.restart_pending.lock().ok()?;
    pending
        .iter()
        .find(|(p, _)| *p == provider)
        .map(|(_, version)| version.clone())
}

fn set_pending_restart(app: &AppHandle, provider: Provider, version: Option<String>) {
    let updater = app.state::<CliUpdater>();
    let Ok(mut pending) = updater.restart_pending.lock() else {
        return;
    };
    pending.retain(|(p, _)| *p != provider);
    pending.extend(version.map(|version| (provider, version)));
}

async fn blocking<T: Send + 'static>(
    work: impl FnOnce() -> T + Send + 'static,
) -> Result<T, String> {
    tauri::async_runtime::spawn_blocking(work)
        .await
        .map_err(|_| "the CLI update stopped unexpectedly".to_string())
}

/// Installs `version` of the provider's CLI into its shared CLI volume through
/// one of its workers. Processes already running keep their loaded CLI.
fn install(container: &str, provider: Provider, version: &str) -> Result<(), String> {
    if parse_version(version).is_none()
        || !version
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b"._-".contains(&b))
    {
        return Err("the CLI version is invalid".into());
    }
    let install = docker::docker_command()
        .args([
            "exec",
            container,
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
    Ok(())
}

/// Whether no worker is launching or running a turn. A worker that cannot
/// answer (an image without the activity check, say) counts as busy.
fn workers_idle(containers: &[String]) -> bool {
    containers.iter().all(|container| {
        docker::docker_command()
            .args(["exec", container, "/worker", "activity"])
            .output()
            .ok()
            .filter(|output| output.status.success())
            .is_some_and(|output| idle_answer(&String::from_utf8_lossy(&output.stdout)))
    })
}

/// A worker's `activity` answer is the number of attempts in flight.
fn idle_answer(output: &str) -> bool {
    output.trim().parse::<u64>() == Ok(0)
}

fn restart(containers: &[String]) -> Result<(), String> {
    for container in containers {
        let restart = docker::docker_command()
            .args(["restart", container])
            .output()
            .map_err(|e| format!("restart a worker: {e}"))?;
        if !restart.status.success() {
            return Err("a worker did not restart after the update".into());
        }
    }
    Ok(())
}

/// Wait until every restarted worker reports healthy again.
fn wait_until_healthy(containers: &[String]) -> Result<(), String> {
    let deadline = Instant::now() + HEALTHY_TIMEOUT;
    for container in containers {
        loop {
            let output = docker::docker_command()
                .args([
                    "inspect",
                    "--format",
                    "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}",
                    container,
                ])
                .output()
                .map_err(|e| format!("inspect a worker: {e}"))?;
            let status = String::from_utf8_lossy(&output.stdout);
            if matches!(status.trim(), "healthy" | "running") {
                break;
            }
            if Instant::now() >= deadline {
                return Err("a worker did not become healthy after the update".into());
            }
            thread::sleep(HEALTH_POLL);
        }
    }
    Ok(())
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

    #[test]
    fn a_newer_release_is_installed_and_then_restarted_onto() {
        assert_eq!(
            plan("2.1.270", Some("2.2.0"), None),
            Plan::Install("2.2.0".into())
        );
        // Installed, but the workers have not restarted yet.
        assert_eq!(
            plan("2.2.0", Some("2.2.0"), Some("2.2.0")),
            Plan::Restart("2.2.0".into())
        );
        // An even newer release while the restart waited replaces it.
        assert_eq!(
            plan("2.2.0", Some("2.3.0"), Some("2.2.0")),
            Plan::Install("2.3.0".into())
        );
    }

    #[test]
    fn current_or_unknown_releases_change_nothing() {
        assert_eq!(plan("2.2.0", Some("2.2.0"), None), Plan::Nothing);
        assert_eq!(plan("2.3.0", Some("2.2.0"), None), Plan::Nothing);
        // Offline: nothing new to install, but a pending restart still runs.
        assert_eq!(plan("2.2.0", None, None), Plan::Nothing);
        assert_eq!(
            plan("2.2.0", None, Some("2.2.0")),
            Plan::Restart("2.2.0".into())
        );
        // The volume moved past the pending version (the image was rebuilt).
        assert_eq!(plan("2.4.0", None, Some("2.2.0")), Plan::Nothing);
    }

    #[test]
    fn only_a_zero_activity_answer_is_idle() {
        assert!(idle_answer("0\n"));
        assert!(!idle_answer("2\n"));
        assert!(!idle_answer(""));
        assert!(!idle_answer("unknown command"));
    }
}
