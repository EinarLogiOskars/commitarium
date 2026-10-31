//! Commitarium's own check that an app's preview runs (ADR-028).
//!
//! Agents have no Docker, so a work order that sets the preview up cannot
//! test its compose file. The coordinator records a preview check of the
//! exact commit the agent published; this module claims it, builds and
//! starts that commit's compose file in a temporary, isolated compose
//! project (validated and rewritten like any preview), waits until every
//! service stays up, healthchecks pass, and the open target answers HTTP,
//! collects what each container did, tears it all down, and reports back.
//! As with isolated validation (ADR-010), the renderer passes only the
//! check's opaque ID; everything else comes from the coordinator.

use crate::preview::{
    self, ComposeRuntime, ComposeServiceState, ProjectHandoff, RunOpen, ServiceEvidence,
    ToolchainManifest,
};
use crate::preview_compose;
use serde::{Deserialize, Serialize};
use std::collections::VecDeque;
use std::fs;
use std::path::Path;
use std::process::Stdio;
use std::sync::{mpsc, Mutex};
use std::time::{Duration, Instant};

/// Ends the compose project name of a project's preview check, apart from
/// its preview and its sessions' previews.
pub(crate) const CHECK_SUFFIX: &str = "-check";
/// How long building the images and creating the containers may take.
const BUILD_TIMEOUT: Duration = Duration::from_secs(20 * 60);
/// How long the containers have to come up and answer once created.
const START_TIMEOUT: Duration = Duration::from_secs(5 * 60);
/// How long a passing stack must keep passing, to catch a service that
/// crashes right after it starts.
const STABLE_FOR: Duration = Duration::from_secs(10);
const POLL: Duration = Duration::from_secs(1);
/// The lines of build and start output kept for a check that failed there.
const OUTPUT_LINES: usize = 200;

const STAGE_SOURCE: &str = "source";
const STAGE_SETUP: &str = "setup";
const STAGE_BUILD: &str = "build";
const STAGE_START: &str = "start";
const STAGE_READY: &str = "ready";

/// The compose ID of a project's preview check.
pub(crate) fn check_compose_id(project_id: &str) -> String {
    format!("{project_id}{CHECK_SUFFIX}")
}

/// What a check saw, as the coordinator records it.
#[derive(Clone, Debug, Default, Deserialize, PartialEq, Serialize)]
pub struct Evidence {
    stage: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    reason: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    open: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    output: String,
    #[serde(default)]
    services: Vec<ServiceEvidence>,
}

impl Evidence {
    fn failed(stage: &str, reason: &str) -> Self {
        Self {
            stage: stage.into(),
            reason: preview::bounded(reason),
            ..Self::default()
        }
    }
}

#[derive(Deserialize)]
struct PreviewCheck {
    id: String,
    project_id: String,
    commit_id: String,
    status: String,
    #[serde(default)]
    evidence: Option<Evidence>,
}

#[derive(Serialize)]
struct Completion<'a> {
    passed: bool,
    evidence: &'a Evidence,
}

#[derive(Debug, Serialize)]
pub struct PreviewCheckRun {
    check_id: String,
    status: String,
}

/// One check at a time: each builds images and starts a whole app.
static CHECKS: Mutex<()> = Mutex::new(());

pub async fn run_preview_check(check_id: String) -> Result<PreviewCheckRun, String> {
    if !safe_check_id(&check_id) {
        return Err("preview check ID is invalid".into());
    }
    let client = reqwest::Client::new();
    let check: PreviewCheck = preview::coordinator_json(
        client
            .post(preview::coordinator_url(&format!(
                "/api/v1/preview-checks/{check_id}/claim"
            )))
            .body(Vec::new())
            .send()
            .await
            .map_err(|e| format!("claim preview check: {e}"))?,
        "preview check",
    )
    .await?;
    if check.id != check_id
        || preview::validate_identifier("project ID", &check.project_id).is_err()
        || !preview::safe_commit(&check.commit_id)
    {
        return Err("coordinator returned an unsafe preview check".into());
    }
    let (passed, evidence) = match check.status.as_str() {
        "running" => {
            let (repository, open) = check_inputs(&client, &check.project_id).await?;
            let project_id = check.project_id.clone();
            let commit_id = check.commit_id.clone();
            crate::runtime::spawn_blocking(move || {
                let _one_at_a_time = CHECKS
                    .lock()
                    .unwrap_or_else(|poisoned| poisoned.into_inner());
                run_check(&project_id, &commit_id, &repository, open)
            })
            .await
            .map_err(|_| "preview check task stopped unexpectedly".to_string())?
        }
        // A finished check is completed again, so the coordinator can move its
        // work order on after an uncertain response, without running the app
        // twice.
        "passed" | "failed" => (
            check.status == "passed",
            check
                .evidence
                .clone()
                .unwrap_or_else(|| Evidence::failed(STAGE_START, "")),
        ),
        status => {
            return Ok(PreviewCheckRun {
                check_id,
                status: status.into(),
            })
        }
    };
    let completed: PreviewCheck = preview::coordinator_json(
        client
            .post(preview::coordinator_url(&format!(
                "/api/v1/preview-checks/{check_id}/complete"
            )))
            .json(&Completion {
                passed,
                evidence: &evidence,
            })
            .send()
            .await
            .map_err(|e| format!("record preview check result: {e}"))?,
        "preview check result",
    )
    .await?;
    Ok(PreviewCheckRun {
        check_id,
        status: completed.status,
    })
}

/// The repository to fetch the commit from, and the stack's open target.
async fn check_inputs(
    client: &reqwest::Client,
    project_id: &str,
) -> Result<(preview::Repository, Option<RunOpen>), String> {
    let manifest: ToolchainManifest = preview::coordinator_json(
        client
            .get(preview::coordinator_url(&format!(
                "/api/v1/projects/{project_id}/toolchain"
            )))
            .send()
            .await
            .map_err(|e| format!("request project stack from coordinator: {e}"))?,
        "project stack",
    )
    .await?;
    if let Some(run) = &manifest.run {
        preview::validate_run(run)?;
    }
    let handoff: ProjectHandoff = preview::coordinator_json(
        client
            .get(preview::coordinator_url(&format!(
                "/api/v1/projects/{project_id}/handoff"
            )))
            .send()
            .await
            .map_err(|e| format!("request project handoff from coordinator: {e}"))?,
        "project handoff",
    )
    .await?;
    preview::validate_handoff(&handoff, project_id)?;
    Ok((
        handoff.source.repository.clone(),
        manifest.run.map(|run| run.open),
    ))
}

fn safe_check_id(value: &str) -> bool {
    value.len() <= 80
        && value
            .strip_prefix("pvc_")
            .is_some_and(|rest| !rest.is_empty() && rest.bytes().all(|b| b.is_ascii_hexdigit()))
}

/// Fetches the commit, checks it, and removes everything it created.
fn run_check(
    project_id: &str,
    commit_id: &str,
    repository: &preview::Repository,
    open: Option<RunOpen>,
) -> (bool, Evidence) {
    let root = match preview::prepare_commit_source(repository, commit_id) {
        Ok(root) => root,
        Err(error) => return (false, Evidence::failed(STAGE_SOURCE, &error)),
    };
    let outcome = check_source(project_id, &root, open);
    preview::remove_source(&root);
    outcome
}

fn check_source(project_id: &str, root: &Path, open: Option<RunOpen>) -> (bool, Evidence) {
    let source = root.join("source");
    let compose_id = check_compose_id(project_id);
    let configured = open.map(|open| preview_compose::OpenTarget {
        service: open.service,
        port: open.port,
    });
    // The stack's open target may predate the file the agent just wrote;
    // then the check opens what the file declares.
    let prepared =
        match preview_compose::normalize_and_rewrite(&source, &compose_id, configured.as_ref()) {
            Err(error) if configured.is_some() && error.contains("preview open") => {
                preview_compose::normalize_and_rewrite(&source, &compose_id, None)
            }
            prepared => prepared,
        };
    let prepared = match prepared {
        Ok(prepared) => prepared,
        Err(error) => return (false, Evidence::failed(STAGE_SETUP, &error)),
    };
    let runtime = ComposeRuntime {
        project_name: prepared.project_name.clone(),
        compose_file: root.join("check.compose.json"),
        environment_file: root.join("check.env"),
        source_root: root.to_path_buf(),
    };
    if let Err(error) =
        preview_compose::write_rewritten(&prepared, &runtime.compose_file).and_then(|()| {
            fs::write(&runtime.environment_file, [])
                .map_err(|e| format!("write isolated preview environment file: {e}"))
        })
    {
        return (false, Evidence::failed(STAGE_SETUP, &error));
    }
    let open = RunOpen {
        service: prepared.open.service.clone(),
        port: prepared.open.port,
    };
    // Leftovers of a check the app quit during go first.
    tear_down(&runtime);
    let started = start_and_watch(&runtime, &prepared.published_ports, &open);
    let services = preview::collect_evidence(&runtime);
    tear_down(&runtime);
    let opened = format!("{}:{}", open.service, open.port);
    match started {
        Ok(()) => (
            true,
            Evidence {
                stage: STAGE_READY.into(),
                open: opened,
                services,
                ..Evidence::default()
            },
        ),
        Err(failure) => (false, failure.evidence(opened, services)),
    }
}

/// Why a check failed after its compose file was accepted.
#[derive(Debug, PartialEq)]
struct StartFailure {
    /// The build and start output, for a failure before the containers ran.
    output: String,
    reason: String,
    building: bool,
}

impl StartFailure {
    /// A failure while building and creating the containers is a build
    /// failure unless a container already failed: a dependency that exited
    /// or turned unhealthy also stops `docker compose up`.
    fn evidence(self, open: String, services: Vec<ServiceEvidence>) -> Evidence {
        let container_failed = services.iter().any(|service| {
            service.exit_code != 0 || service.state == "restarting" || service.health == "unhealthy"
        });
        let stage = if self.building && !container_failed {
            STAGE_BUILD
        } else {
            STAGE_START
        };
        Evidence {
            stage: stage.into(),
            reason: preview::bounded(&self.reason),
            open,
            output: self.output,
            services,
        }
    }
}

fn start_and_watch(
    runtime: &ComposeRuntime,
    ports: &[preview_compose::PublishedPort],
    open: &RunOpen,
) -> Result<(), StartFailure> {
    let failure = |reason: String| StartFailure {
        output: String::new(),
        reason,
        building: false,
    };
    compose_up(runtime)?;
    let urls = preview::resolve_urls(runtime, ports, open).map_err(failure)?;
    let port = urls
        .iter()
        .find_map(|url| url.open_port())
        .ok_or_else(|| failure("the preview open target has no URL".into()))?;
    let mut settle = Settle::default();
    let deadline = Instant::now() + START_TIMEOUT;
    loop {
        let services = preview::compose_states(runtime).map_err(failure)?;
        let decision = decide(
            &preview::dedupe_services(services),
            open,
            preview::http_responds(port),
        );
        if let Some(outcome) = settle.observe(decision, Instant::now()) {
            return outcome.map_err(failure);
        }
        if Instant::now() >= deadline {
            return Err(failure(settle.timed_out()));
        }
        std::thread::sleep(POLL);
    }
}

/// Builds the images and creates the containers, within BUILD_TIMEOUT.
fn compose_up(runtime: &ComposeRuntime) -> Result<(), StartFailure> {
    let fail = |output: &VecDeque<String>, reason: String| StartFailure {
        output: output.iter().cloned().collect::<Vec<_>>().join("\n"),
        reason,
        building: true,
    };
    let mut output = VecDeque::new();
    let mut child = match preview::compose_command(runtime)
        .args(["--progress", "plain", "up", "-d", "--build"])
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
    {
        Ok(child) => child,
        Err(error) => {
            return Err(fail(
                &output,
                format!("start the app's containers: {error}"),
            ))
        }
    };
    let (sender, receiver) = mpsc::channel();
    if let Some(stdout) = child.stdout.take() {
        preview::spawn_log_reader(stdout, sender.clone());
    }
    if let Some(stderr) = child.stderr.take() {
        preview::spawn_log_reader(stderr, sender.clone());
    }
    drop(sender);
    let deadline = Instant::now() + BUILD_TIMEOUT;
    let status = loop {
        while let Ok(line) = receiver.try_recv() {
            keep_line(&mut output, line);
        }
        match child.try_wait() {
            Ok(Some(status)) => break status,
            Ok(None) if Instant::now() >= deadline => {
                let _ = child.kill();
                let _ = child.wait();
                return Err(fail(
                    &output,
                    "building and starting the app took longer than 20 minutes".into(),
                ));
            }
            Ok(None) => std::thread::sleep(Duration::from_millis(200)),
            Err(error) => {
                return Err(fail(
                    &output,
                    format!("wait for the app's containers: {error}"),
                ))
            }
        }
    };
    // The readers end when the process closes its output.
    for line in receiver {
        keep_line(&mut output, line);
    }
    if status.success() {
        return Ok(());
    }
    let last = output
        .iter()
        .rev()
        .find(|line| !line.trim().is_empty())
        .cloned()
        .unwrap_or_default();
    Err(fail(
        &output,
        format!("docker compose up failed: {}", last.trim()),
    ))
}

fn keep_line(output: &mut VecDeque<String>, line: String) {
    if line.trim().is_empty() {
        return;
    }
    if output.len() == OUTPUT_LINES {
        output.pop_front();
    }
    output.push_back(line);
}

/// Removes the check's containers, network, and volumes; its images stay as
/// build cache, labelled for the project's deletion.
fn tear_down(runtime: &ComposeRuntime) {
    let _ = preview::compose_capture(
        runtime,
        &preview::compose_down_args(true, false),
        "remove the preview check",
    );
    for kind in ["container", "network", "volume"] {
        preview::remove_labeled_resources(kind, &runtime.project_name);
    }
    // A rebuilt check leaves its previous images untagged.
    crate::cleanup::spawn();
}

/// What one look at the stack says.
#[derive(Debug, PartialEq)]
enum Decision {
    /// Not there yet: waiting for what it names.
    Wait(String),
    Pass,
    Fail(String),
}

/// Decides one look at the stack: a service that exited with an error,
/// keeps restarting, or is unhealthy fails the check; it passes once the
/// open service runs, no healthcheck is still starting, and the open target
/// answers HTTP.
fn decide(services: &[ComposeServiceState], open: &RunOpen, answering: bool) -> Decision {
    if let Some(problem) = preview::stack_problem(services, &open.service) {
        return Decision::Fail(problem);
    }
    if let Some(service) = services
        .iter()
        .find(|service| service.health == "unhealthy")
    {
        return Decision::Fail(format!(
            "the preview service {:?} is unhealthy",
            service.service
        ));
    }
    if !services
        .iter()
        .any(|service| service.service == open.service && service.state == "running")
    {
        return Decision::Wait(format!("the open service {:?} to run", open.service));
    }
    if let Some(service) = services.iter().find(|service| service.health == "starting") {
        return Decision::Wait(format!("the healthcheck of {:?} to pass", service.service));
    }
    if !answering {
        return Decision::Wait(format!("{}:{} to answer HTTP", open.service, open.port));
    }
    Decision::Pass
}

/// Turns looks at the stack into an outcome: a pass must hold for
/// STABLE_FOR, and a failure ends the check at once.
#[derive(Default)]
struct Settle {
    passing_since: Option<Instant>,
    waiting_for: String,
}

impl Settle {
    fn observe(&mut self, decision: Decision, now: Instant) -> Option<Result<(), String>> {
        match decision {
            Decision::Fail(reason) => Some(Err(reason)),
            Decision::Wait(what) => {
                self.passing_since = None;
                self.waiting_for = what;
                None
            }
            Decision::Pass => {
                let since = *self.passing_since.get_or_insert(now);
                self.waiting_for = "the app to keep running".into();
                (now.duration_since(since) >= STABLE_FOR).then_some(Ok(()))
            }
        }
    }

    fn timed_out(&self) -> String {
        let minutes = START_TIMEOUT.as_secs() / 60;
        if self.waiting_for.is_empty() {
            format!("the app did not start within {minutes} minutes")
        } else {
            format!(
                "the app did not start within {minutes} minutes: still waiting for {}",
                self.waiting_for
            )
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn state(service: &str, state: &str, exit_code: i64, health: &str) -> ComposeServiceState {
        ComposeServiceState {
            service: service.into(),
            state: state.into(),
            exit_code,
            health: health.into(),
        }
    }

    fn web() -> RunOpen {
        RunOpen {
            service: "web".into(),
            port: 5173,
        }
    }

    #[test]
    fn a_running_stack_that_answers_passes() {
        let services = [
            state("web", "running", 0, ""),
            state("db", "running", 0, "healthy"),
            state("migrate", "exited", 0, ""),
        ];
        assert_eq!(decide(&services, &web(), true), Decision::Pass);
        assert_eq!(
            decide(&services, &web(), false),
            Decision::Wait("web:5173 to answer HTTP".into())
        );
    }

    #[test]
    fn a_container_that_stops_or_is_unhealthy_fails_the_check() {
        let exited = [
            state("web", "running", 0, ""),
            state("api", "exited", 1, ""),
        ];
        assert_eq!(
            decide(&exited, &web(), true),
            Decision::Fail("the preview service \"api\" exited with code 1".into())
        );
        let restarting = [state("web", "restarting", 137, "")];
        assert_eq!(
            decide(&restarting, &web(), false),
            Decision::Fail("the preview service \"web\" keeps restarting (exit code 137)".into())
        );
        let unhealthy = [
            state("web", "running", 0, ""),
            state("db", "running", 0, "unhealthy"),
        ];
        assert_eq!(
            decide(&unhealthy, &web(), true),
            Decision::Fail("the preview service \"db\" is unhealthy".into())
        );
        let open_stopped = [state("web", "exited", 0, "")];
        assert_eq!(
            decide(&open_stopped, &web(), false),
            Decision::Fail("the preview open service \"web\" stopped".into())
        );
    }

    #[test]
    fn healthchecks_and_the_open_service_are_waited_for() {
        let starting = [
            state("web", "running", 0, ""),
            state("db", "running", 0, "starting"),
        ];
        assert_eq!(
            decide(&starting, &web(), true),
            Decision::Wait("the healthcheck of \"db\" to pass".into())
        );
        let created = [state("web", "created", 0, "")];
        assert_eq!(
            decide(&created, &web(), false),
            Decision::Wait("the open service \"web\" to run".into())
        );
    }

    #[test]
    fn a_pass_must_hold_before_the_check_passes() {
        let start = Instant::now();
        let mut settle = Settle::default();
        assert_eq!(settle.observe(Decision::Pass, start), None);
        // A crash right after starting resets nothing: it fails.
        assert_eq!(
            Settle {
                passing_since: Some(start),
                waiting_for: String::new()
            }
            .observe(
                Decision::Fail("the preview service \"api\" exited with code 1".into()),
                start + STABLE_FOR
            ),
            Some(Err("the preview service \"api\" exited with code 1".into()))
        );
        // Waiting again restarts the stable period.
        assert_eq!(
            settle.observe(
                Decision::Wait("web:5173 to answer HTTP".into()),
                start + Duration::from_secs(5)
            ),
            None
        );
        assert_eq!(
            settle.observe(Decision::Pass, start + Duration::from_secs(6)),
            None
        );
        assert_eq!(
            settle.observe(Decision::Pass, start + Duration::from_secs(6) + STABLE_FOR),
            Some(Ok(()))
        );
    }

    #[test]
    fn a_timeout_says_what_it_waited_for() {
        let mut settle = Settle::default();
        settle.observe(
            Decision::Wait("the healthcheck of \"db\" to pass".into()),
            Instant::now(),
        );
        assert_eq!(
            settle.timed_out(),
            "the app did not start within 5 minutes: still waiting for the healthcheck of \"db\" to pass"
        );
    }

    #[test]
    fn build_failures_are_told_from_containers_that_failed() {
        let failure = || StartFailure {
            output: "#5 ERROR: failed to solve".into(),
            reason: "docker compose up failed: failed to solve".into(),
            building: true,
        };
        let built = failure().evidence("web:5173".into(), Vec::new());
        assert_eq!(built.stage, STAGE_BUILD);
        assert_eq!(built.output, "#5 ERROR: failed to solve");
        let dependency = failure().evidence(
            "web:5173".into(),
            vec![ServiceEvidence {
                service: "db".into(),
                state: "running".into(),
                exit_code: 0,
                health: "unhealthy".into(),
                logs: "FATAL: password authentication failed\n".into(),
            }],
        );
        assert_eq!(dependency.stage, STAGE_START);
    }

    #[test]
    fn evidence_matches_the_coordinator_contract() {
        let evidence = Evidence {
            stage: STAGE_START.into(),
            reason: "the preview service \"api\" exited with code 1".into(),
            open: "web:5173".into(),
            output: String::new(),
            services: vec![ServiceEvidence {
                service: "api".into(),
                state: "exited".into(),
                exit_code: 1,
                health: String::new(),
                logs: "boom\n".into(),
            }],
        };
        let encoded = serde_json::to_value(&evidence).unwrap();
        assert_eq!(
            encoded,
            serde_json::json!({
                "stage": "start",
                "reason": "the preview service \"api\" exited with code 1",
                "open": "web:5173",
                "services": [{"service": "api", "state": "exited", "exit_code": 1, "logs": "boom\n"}],
            })
        );
        let decoded: Evidence = serde_json::from_value(encoded).unwrap();
        assert_eq!(decoded, evidence);
    }

    #[test]
    fn check_ids_and_compose_projects_are_strict() {
        assert!(safe_check_id("pvc_0123456789abcdef0123456789abcdef"));
        assert!(!safe_check_id("pvc_"));
        assert!(!safe_check_id("pvc_../x"));
        assert!(!safe_check_id("val_0123"));
        assert_eq!(
            preview_compose::project_name(&check_compose_id("prj_abc")).unwrap(),
            "commitarium-preview-prj_abc-check"
        );
    }
}
