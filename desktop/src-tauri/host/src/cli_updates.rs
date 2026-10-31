//! Keeping the agents' provider CLIs (Codex, Claude Code) current between
//! image builds. They are Commitarium's own tooling, so they always run the
//! newest release: a background task checks at launch and every few hours,
//! installs a newer CLI into the provider's shared CLI volume (the workers
//! prefer it), and restarts the provider's workers once none of them is
//! running a turn. New CLI versions also bring new models.
//!
//! The same task keeps an approved environment's derivative images (ADR-0010)
//! built from the current base worker images, so the workers do not keep
//! running old worker code after the bases are rebuilt or updated.

use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

use serde::Serialize;
use tokio::sync::Notify;

use crate::agents::{self, Provider, CLI_DIRECTORY};
use crate::environment::{self, EnvironmentPlan};
use crate::events::Events;
use crate::{docker, profiles};

const REGISTRY_TIMEOUT: Duration = Duration::from_secs(10);
/// How often the CLIs are checked once they are current.
const CHECK_INTERVAL: Duration = Duration::from_secs(6 * 60 * 60);
/// How soon a pass is retried while an update waits for running work, failed,
/// or no worker was up to check.
const RETRY_INTERVAL: Duration = Duration::from_secs(3 * 60);
/// How soon a failed environment rebuild (a package that no longer installs,
/// say) is tried again, unless the stack comes up or updates first.
const REBUILD_RETRY_INTERVAL: Duration = Duration::from_secs(30 * 60);
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
    /// Whether this pass reached the npm registry. When it did not, a current
    /// CLI may yet have a newer release; the check is retried.
    checked: bool,
}

/// The updater's shared state. A single background task does every update,
/// so two never overlap.
#[derive(Default)]
pub struct CliUpdater {
    statuses: Mutex<Vec<CliStatus>>,
    /// Versions installed whose workers still wait for a restart. Only
    /// workers that do not report the CLI version they started with need it.
    restart_pending: Mutex<Vec<(Provider, String)>>,
    /// Versions each provider's workers were already restarted onto. If they
    /// still report another version afterwards, the worker and the updater
    /// read different binaries; restarting again would only loop.
    restarted_onto: Mutex<Vec<(Provider, String)>>,
    /// When a failed environment rebuild may be tried again.
    rebuild_after: Mutex<Option<Instant>>,
    wake: Notify,
}

/// What the background updater works with.
struct Task {
    updater: Arc<CliUpdater>,
    manager: profiles::ProfileManager,
    events: Events,
}

/// The agents' CLI versions and any update in progress.
pub fn agent_cli_status(updater: &CliUpdater) -> Vec<CliStatus> {
    updater
        .statuses
        .lock()
        .map(|s| s.clone())
        .unwrap_or_default()
}

/// Check now, for example because the stack (and so the workers) just came up.
/// New base images may also have arrived, so a failed environment rebuild is
/// tried again at once.
pub fn wake(updater: &CliUpdater) {
    if let Ok(mut rebuild_after) = updater.rebuild_after.lock() {
        *rebuild_after = None;
    }
    updater.wake.notify_one();
}

/// Start the background updater. It never blocks startup; a failed pass is
/// logged and retried.
pub fn spawn(updater: Arc<CliUpdater>, manager: profiles::ProfileManager, events: Events) {
    let task = Task {
        updater,
        manager,
        events,
    };
    crate::runtime::spawn(async move {
        loop {
            let delay = match update_pass(&task).await {
                Ok(true) => CHECK_INTERVAL,
                Ok(false) => RETRY_INTERVAL,
                Err(error) => {
                    eprintln!("[cli-updates] {error}");
                    RETRY_INTERVAL
                }
            };
            let _ = tokio::time::timeout(delay, task.updater.wake.notified()).await;
        }
    });
}

/// One pass: the approved environment first, so a recreate it needs comes
/// before any CLI restart, then the CLIs. Returns whether everything is
/// settled.
async fn update_pass(task: &Task) -> Result<bool, String> {
    let environment = match environment_pass(task).await {
        Ok(settled) => settled,
        Err(error) => {
            eprintln!("[environment-images] {error}");
            false
        }
    };
    Ok(cli_pass(task).await? && environment)
}

/// Keep the approved environment on the current base worker images: rebuild
/// stale derivatives with the approved package union, then recreate the
/// workers once none is running a turn. Returns whether it is settled.
async fn environment_pass(task: &Task) -> Result<bool, String> {
    let workers = blocking(|| {
        PROVIDERS
            .into_iter()
            .map(|provider| running_workers(provider).map(|containers| (provider, containers)))
            .collect::<Result<Vec<_>, String>>()
    })
    .await??;
    // Bringing the stack up wakes this pass once there are workers to follow.
    if workers.iter().all(|(_, containers)| containers.is_empty()) {
        return Ok(true);
    }
    let mut plan = environment_plan(&workers).await?;
    if plan == EnvironmentPlan::Rebuild {
        let deferred = task
            .updater
            .rebuild_after
            .lock()
            .ok()
            .and_then(|after| *after)
            .is_some_and(|after| Instant::now() < after);
        if deferred {
            return Ok(false);
        }
        // The same union provisioning builds, read without touching a request.
        let packages = environment::approved_packages().await?;
        eprintln!(
            "[environment-images] a base worker image changed; rebuilding the approved environment"
        );
        let rebuilt = blocking(move || environment::rebuild_environment_images(&packages)).await?;
        if let Err(error) = rebuilt {
            if let Ok(mut after) = task.updater.rebuild_after.lock() {
                *after = Some(Instant::now() + REBUILD_RETRY_INTERVAL);
            }
            return Err(error);
        }
        plan = environment_plan(&workers).await?;
    }
    match plan {
        EnvironmentPlan::Current => Ok(true),
        // A base changed again during the rebuild; the next pass follows it.
        EnvironmentPlan::Rebuild => Ok(false),
        EnvironmentPlan::Recreate => {
            let containers: Vec<String> = workers
                .into_iter()
                .flat_map(|(_, containers)| containers)
                .collect();
            // Recreating ends running turns; wait until there are none.
            if !blocking(move || workers_idle(&containers)).await? {
                return Ok(false);
            }
            let manager = task.manager.clone();
            blocking(move || environment::recreate_environment_workers(&manager)).await??;
            eprintln!("[environment-images] the workers now run the rebuilt environment");
            Ok(true)
        }
    }
}

async fn environment_plan(workers: &[(Provider, Vec<String>)]) -> Result<EnvironmentPlan, String> {
    let workers = workers.to_vec();
    blocking(move || {
        environment::inspect_environment(&workers)
            .map(|images| environment::environment_plan(images.as_deref()))
    })
    .await?
}

/// One pass over both providers' CLIs. Returns whether everything is settled:
/// each provider with a running worker is current and at least one was
/// checked.
async fn cli_pass(task: &Task) -> Result<bool, String> {
    let mut settled = true;
    let mut inspected = false;
    for provider in PROVIDERS {
        let containers = blocking(move || running_workers(provider)).await??;
        let Some(first) = containers.first().cloned() else {
            set_status(task, provider, None);
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
        inspected = true;
        let latest = match latest_version(provider).await {
            Ok(latest) => Some(latest),
            Err(error) => {
                eprintln!("[cli-updates] {error}");
                settled = false;
                None
            }
        };
        let checked = latest.is_some();
        let started = blocking({
            let containers = containers.clone();
            move || {
                containers
                    .iter()
                    .map(|container| worker_activity(container).cli_version)
                    .collect::<Vec<_>>()
            }
        })
        .await?;
        let pending = pending_restart(task, provider);
        let (target, needs_install) = match plan(
            &installed,
            latest.as_deref(),
            &started,
            pending.as_deref(),
        ) {
            Plan::Nothing => {
                set_pending_restart(task, provider, None);
                set_status(task, provider, Some(current(provider, &installed, checked)));
                continue;
            }
            Plan::Install(version) => {
                set_restarted_onto(task, provider, None);
                (version, true)
            }
            Plan::Restart(version) => {
                if restarted_onto(task, provider).as_deref() == Some(version.as_str()) {
                    eprintln!(
                            "[cli-updates] {} workers still report another CLI version after restarting onto {version}; not restarting again",
                            provider.executable()
                        );
                    set_pending_restart(task, provider, None);
                    set_status(task, provider, Some(current(provider, &installed, checked)));
                    continue;
                }
                (version, false)
            }
        };
        let status = |state| CliStatus {
            provider,
            installed: installed.clone(),
            target: Some(target.clone()),
            state,
            checked,
        };
        if needs_install {
            set_status(task, provider, Some(status(CliState::Updating)));
            let result = blocking({
                let (first, version) = (first.clone(), target.clone());
                move || install(&first, provider, &version)
            })
            .await?;
            if let Err(error) = result {
                eprintln!("[cli-updates] {error}");
                set_status(task, provider, Some(status(CliState::Behind)));
                settled = false;
                continue;
            }
            set_pending_restart(task, provider, Some(target.clone()));
        }
        // Installing left running turns alone; restarting would end them.
        let idle = blocking({
            let containers = containers.clone();
            move || workers_idle(&containers)
        })
        .await?;
        if !idle {
            set_status(task, provider, Some(status(CliState::Waiting)));
            settled = false;
            continue;
        }
        set_status(task, provider, Some(status(CliState::Updating)));
        let restarted = blocking(move || {
            restart(&containers)?;
            wait_until_healthy(&containers)
        })
        .await?;
        if let Err(error) = restarted {
            eprintln!("[cli-updates] {error}");
            set_status(task, provider, Some(status(CliState::Behind)));
            settled = false;
            continue;
        }
        set_pending_restart(task, provider, None);
        set_restarted_onto(task, provider, Some(target.clone()));
        set_status(task, provider, Some(current(provider, &target, checked)));
    }
    Ok(settled && inspected)
}

fn current(provider: Provider, installed: &str, checked: bool) -> CliStatus {
    CliStatus {
        provider,
        installed: installed.to_string(),
        target: None,
        state: CliState::Current,
        checked,
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
/// install it already reads the new version while the workers still run the
/// one they started with. `started` is that version for each worker, measured
/// rather than remembered, so a restart the app quit before still runs; a
/// worker built before it reports none and relies on the restart recorded in
/// `pending` instead. Without the registry (offline) a needed restart still
/// runs.
fn plan(
    installed: &str,
    latest: Option<&str>,
    started: &[Option<String>],
    pending: Option<&str>,
) -> Plan {
    if let Some(latest) = latest.filter(|latest| newer(latest, installed)) {
        return Plan::Install(latest.to_string());
    }
    if started.iter().flatten().any(|version| version != installed) {
        return Plan::Restart(installed.to_string());
    }
    match pending {
        Some(pending) if started.iter().any(Option::is_none) && !newer(installed, pending) => {
            Plan::Restart(pending.to_string())
        }
        _ => Plan::Nothing,
    }
}

fn set_status(task: &Task, provider: Provider, status: Option<CliStatus>) {
    let Ok(mut statuses) = task.updater.statuses.lock() else {
        return;
    };
    let before = serde_json::to_value(&*statuses).ok();
    statuses.retain(|s| s.provider != provider);
    statuses.extend(status);
    statuses.sort_by_key(|s| PROVIDERS.iter().position(|p| *p == s.provider));
    if serde_json::to_value(&*statuses).ok() != before {
        task.events.publish(STATUS_EVENT, &*statuses);
    }
}

fn pending_restart(task: &Task, provider: Provider) -> Option<String> {
    let pending = task.updater.restart_pending.lock().ok()?;
    pending
        .iter()
        .find(|(p, _)| *p == provider)
        .map(|(_, version)| version.clone())
}

fn set_pending_restart(task: &Task, provider: Provider, version: Option<String>) {
    let Ok(mut pending) = task.updater.restart_pending.lock() else {
        return;
    };
    pending.retain(|(p, _)| *p != provider);
    pending.extend(version.map(|version| (provider, version)));
}

fn restarted_onto(task: &Task, provider: Provider) -> Option<String> {
    let restarted = task.updater.restarted_onto.lock().ok()?;
    restarted
        .iter()
        .find(|(p, _)| *p == provider)
        .map(|(_, version)| version.clone())
}

fn set_restarted_onto(task: &Task, provider: Provider, version: Option<String>) {
    let Ok(mut restarted) = task.updater.restarted_onto.lock() else {
        return;
    };
    restarted.retain(|(p, _)| *p != provider);
    restarted.extend(version.map(|version| (provider, version)));
}

async fn blocking<T: Send + 'static>(
    work: impl FnOnce() -> T + Send + 'static,
) -> Result<T, String> {
    crate::runtime::spawn_blocking(work)
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
/// answer counts as busy.
fn workers_idle(containers: &[String]) -> bool {
    containers
        .iter()
        .all(|container| worker_activity(container).idle)
}

/// What a worker is doing, and the CLI version it started with.
#[derive(Debug, Default, Eq, PartialEq)]
struct Activity {
    idle: bool,
    /// Workers built before they report it say nothing.
    cli_version: Option<String>,
}

/// A worker that cannot answer counts as busy.
fn worker_activity(container: &str) -> Activity {
    if !answers_activity(container) {
        return Activity {
            idle: processes_idle(container),
            cli_version: None,
        };
    }
    docker::docker_command()
        .args(["exec", "--env", "COMMITARIUM_ACTIVITY_FORMAT=json"])
        .args([container, "/worker", "activity"])
        .output()
        .ok()
        .filter(|output| output.status.success())
        .and_then(|output| activity_answer(&String::from_utf8_lossy(&output.stdout)))
        .unwrap_or_default()
}

/// Whether no agent's worker, of any provider, is launching or running a turn.
pub fn all_workers_idle() -> Result<bool, String> {
    for provider in PROVIDERS {
        if !workers_idle(&running_workers(provider)?) {
            return Ok(false);
        }
    }
    Ok(true)
}

/// Whether the worker binary has the `activity` subcommand. Workers built
/// before it must never be asked: their binary ignores the argument and
/// starts a second worker service, whose startup recovery runs against the
/// live worker's journal before it fails to bind the port. Its activity URL
/// is compiled into the binary, so a plain search tells them apart without
/// running it.
fn answers_activity(container: &str) -> bool {
    docker::docker_command()
        .args([
            "exec",
            container,
            "grep",
            "-q",
            "-F",
            "/internal/v1/activity",
            "/worker",
        ])
        .output()
        .is_ok_and(|output| output.status.success())
}

/// A worker's `activity` answer: JSON with the number of attempts in flight
/// and the CLI version it started with, or, from a worker built before it,
/// just the number.
fn activity_answer(output: &str) -> Option<Activity> {
    #[derive(serde::Deserialize)]
    struct Answer {
        attempts: u64,
        #[serde(default)]
        cli_version: Option<String>,
    }
    let output = output.trim();
    let answer = match output.parse::<u64>() {
        Ok(attempts) => Answer {
            attempts,
            cli_version: None,
        },
        Err(_) => serde_json::from_str(output).ok()?,
    };
    Some(Activity {
        idle: answer.attempts == 0,
        cli_version: answer
            .cli_version
            .filter(|version| parse_version(version).is_some()),
    })
}

/// The fallback for workers without `activity`, read from outside with
/// `docker top` so nothing runs inside the container. Each turn runs its
/// provider CLI (and git) as child processes of the worker service, so a
/// worker running only its service and health checks is idle. The coordinator
/// has no single view of everything running on an agent (runs, assistant,
/// vision, and setup turns are tracked apart), so the container is the
/// sounder witness. A turn still being launched, before its process starts,
/// is the one moment this misses.
fn processes_idle(container: &str) -> bool {
    docker::docker_command()
        .args(["top", container, "-o", "pid,args"])
        .output()
        .ok()
        .filter(|output| output.status.success())
        .is_some_and(|output| idle_processes(&String::from_utf8_lossy(&output.stdout)))
}

/// `docker top -o pid,args` output: a header, then `<pid> <command line>`.
fn idle_processes(listing: &str) -> bool {
    let mut lines = listing.lines().filter(|line| !line.trim().is_empty());
    lines.next().is_some_and(|header| header.contains("PID"))
        && lines.all(|line| {
            matches!(
                line.trim().split_once(char::is_whitespace),
                Some((_, command)) if matches!(command.trim(), "/worker" | "/worker healthcheck")
            )
        })
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

/// The CLI version a worker runs: an updated one when installed and working,
/// else the image's.
fn installed_version(container: &str, provider: Provider) -> Option<String> {
    let script = agents::cli_script(provider);
    let output = docker::docker_command()
        .args([
            "exec",
            container,
            "sh",
            "-c",
            &script,
            provider.executable(),
            "--version",
        ])
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

    fn started(versions: &[&str]) -> Vec<Option<String>> {
        versions.iter().map(|v| Some(v.to_string())).collect()
    }

    #[test]
    fn a_newer_release_is_installed_and_then_restarted_onto() {
        assert_eq!(
            plan("2.1.270", Some("2.2.0"), &started(&["2.1.270"]), None),
            Plan::Install("2.2.0".into())
        );
        // Installed, but the workers still run the CLI they started with,
        // whether or not this run of the app did the install.
        assert_eq!(
            plan("2.2.0", Some("2.2.0"), &started(&["2.1.270"]), None),
            Plan::Restart("2.2.0".into())
        );
        assert_eq!(
            plan("2.2.0", None, &started(&["2.2.0", "2.1.270"]), None),
            Plan::Restart("2.2.0".into())
        );
        // An even newer release while the restart waited replaces it.
        assert_eq!(
            plan("2.2.0", Some("2.3.0"), &started(&["2.1.270"]), None),
            Plan::Install("2.3.0".into())
        );
    }

    #[test]
    fn workers_on_the_installed_release_change_nothing() {
        assert_eq!(
            plan("2.2.0", Some("2.2.0"), &started(&["2.2.0", "2.2.0"]), None),
            Plan::Nothing
        );
        assert_eq!(
            plan("2.3.0", Some("2.2.0"), &started(&["2.3.0"]), None),
            Plan::Nothing
        );
        // Offline: nothing new to install.
        assert_eq!(
            plan("2.2.0", None, &started(&["2.2.0"]), None),
            Plan::Nothing
        );
        // A remembered restart the workers already reflect is done.
        assert_eq!(
            plan("2.2.0", None, &started(&["2.2.0"]), Some("2.2.0")),
            Plan::Nothing
        );
    }

    #[test]
    fn a_current_cli_says_whether_the_registry_was_reached() {
        // Offline, the Agents page says it could not check rather than "up
        // to date".
        let offline = serde_json::to_value(current(Provider::Claude, "2.1.296", false)).unwrap();
        assert_eq!(offline["state"], "current");
        assert_eq!(offline["checked"], false);
        let online = serde_json::to_value(current(Provider::Claude, "2.1.296", true)).unwrap();
        assert_eq!(online["checked"], true);
    }

    #[test]
    fn workers_that_do_not_report_their_cli_rely_on_the_remembered_restart() {
        assert_eq!(plan("2.2.0", Some("2.2.0"), &[None], None), Plan::Nothing);
        assert_eq!(plan("2.3.0", Some("2.2.0"), &[None], None), Plan::Nothing);
        // Offline: nothing new to install, but a pending restart still runs.
        assert_eq!(plan("2.2.0", None, &[None], None), Plan::Nothing);
        assert_eq!(
            plan("2.2.0", None, &[None], Some("2.2.0")),
            Plan::Restart("2.2.0".into())
        );
        // The volume moved past the pending version (the image was rebuilt).
        assert_eq!(plan("2.4.0", None, &[None], Some("2.2.0")), Plan::Nothing);
    }

    #[test]
    fn activity_answers_old_and_new_are_read() {
        let answer = |idle, version: Option<&str>| {
            Some(Activity {
                idle,
                cli_version: version.map(str::to_string),
            })
        };
        // Workers before the CLI version answer with the bare count.
        assert_eq!(activity_answer("0\n"), answer(true, None));
        assert_eq!(activity_answer("2\n"), answer(false, None));
        assert_eq!(
            activity_answer("{\"busy\":false,\"attempts\":0,\"cli_version\":\"2.1.296\"}\n"),
            answer(true, Some("2.1.296"))
        );
        assert_eq!(
            activity_answer("{\"busy\":true,\"attempts\":1}\n"),
            answer(false, None)
        );
        // A version that is not one is no version.
        assert_eq!(
            activity_answer("{\"busy\":false,\"attempts\":0,\"cli_version\":\"x\"}"),
            answer(true, None)
        );
        assert_eq!(activity_answer(""), None);
        assert_eq!(activity_answer("unknown command"), None);
    }

    #[test]
    fn a_worker_without_activity_is_idle_only_when_running_nothing_else() {
        assert!(idle_processes("PID   COMMAND\n95361   /worker\n"));
        // Health checks come and go every few seconds.
        assert!(idle_processes(
            "PID   COMMAND\n95361   /worker\n95420   /worker healthcheck\n"
        ));
        assert!(!idle_processes(
            "PID   COMMAND\n95361   /worker\n95500   node /usr/local/bin/codex exec --json\n"
        ));
        assert!(!idle_processes(
            "PID   COMMAND\n95361   /worker\n95501   git -C /workspaces/x fetch\n"
        ));
        // No answer at all is not an idle answer.
        assert!(!idle_processes(""));
        assert!(!idle_processes("Error: No such container"));
    }
}
