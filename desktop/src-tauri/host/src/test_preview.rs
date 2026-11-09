//! Throwaway previews that Mayka's helpers test the app in (HELPERS_PLAN.md,
//! H6).
//!
//! The coordinator keeps a record of each test preview a helper asked for;
//! it cannot run Docker, so this module acts on them, like preview checks.
//! It claims a pending one, starts the project's default branch in a compose
//! project of its own (validated and rewritten like any preview, with the
//! test accounts), waits until it answers, and then connects the helper's
//! agent worker to the network of the service the preview opens. The worker
//! reaches that service at its address on that network; the address is
//! reported back, and the helper starts.
//!
//! When the record ends (the helper is done, its project is deleted, or it
//! outlived its time), the worker is disconnected and everything the test
//! preview created is removed. Nothing here is remembered across a restart:
//! test previews are found by their compose project's name, so leftovers of
//! a host that stopped are removed when it starts, and a record that says a
//! test preview runs which this host did not start is reported failed.
//!
//! What the worker gains while connected is the test preview's network: the
//! app's own containers, which hold test data only. Every other turn on that
//! worker shares it for that time, and those containers can reach the
//! worker's listening ports, of which the worker API asks for its token.

use crate::preview::{self, ComposeRuntime, ProjectHandoff, RunOpen, ToolchainManifest};
use crate::{agents, docker, preview_compose};
use serde::{Deserialize, Serialize};
use std::collections::{BTreeMap, HashMap, HashSet};
use std::fs;
use std::net::Ipv4Addr;
use std::path::PathBuf;
use std::sync::Mutex;
use std::time::{Duration, Instant};

const COMPOSE_PROJECT_LABEL: &str = "com.docker.compose.project";
const COMPOSE_SERVICE_LABEL: &str = "com.docker.compose.service";
/// How often compose projects nobody wants are looked for.
const SWEEP_EVERY: Duration = Duration::from_secs(5 * 60);
/// How long the worker has to reach the test preview once connected.
const REACH_TIMEOUT: Duration = Duration::from_secs(30);

/// A test preview as the coordinator lists it.
#[derive(Clone, Debug, Deserialize)]
pub(crate) struct TestPreview {
    pub(crate) id: String,
    pub(crate) project_id: String,
    pub(crate) agent: String,
    pub(crate) status: String,
}

#[derive(Deserialize)]
struct TestPreviews {
    previews: Vec<TestPreview>,
}

#[derive(Serialize)]
struct Ready<'a> {
    address: &'a str,
}

#[derive(Serialize)]
struct Failed<'a> {
    reason: &'a str,
}

/// What this host process does with test previews: those it started (with
/// the directory their sources are in), and those it works on now.
#[derive(Default)]
struct Owned {
    started: HashMap<String, Option<PathBuf>>,
    busy: HashSet<String>,
    swept: Option<Instant>,
}

static OWNED: Mutex<Option<Owned>> = Mutex::new(None);

fn with_owned<T>(change: impl FnOnce(&mut Owned) -> T) -> T {
    let mut guard = OWNED
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    change(guard.get_or_insert_with(Owned::default))
}

/// A helper's ID: `tsk_` and 24 lowercase hex digits.
pub(crate) fn safe_id(value: &str) -> bool {
    value.strip_prefix("tsk_").is_some_and(|rest| {
        rest.len() == 24
            && rest
                .bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
    })
}

/// The compose ID of a test preview, apart from the project's preview, its
/// sessions', its check and its deployment.
fn compose_id(project_id: &str, id: &str) -> String {
    format!("{project_id}-{id}")
}

/// Whether a compose project is a test preview: its name ends in a helper's
/// ID.
pub(crate) fn is_test_project(name: &str) -> bool {
    test_preview_of(name).is_some()
}

/// The helper ID a test preview's compose project ends in.
fn test_preview_of(name: &str) -> Option<&str> {
    let rest = name.strip_prefix(preview::PREVIEW_TEMP_PREFIX)?;
    let at = rest.rfind("-tsk_")?;
    let id = &rest[at + 1..];
    safe_id(id).then_some(id)
}

/// What one look at the coordinator's list calls for.
#[derive(Debug, Default)]
struct Plan {
    /// Pending: claim and start.
    start: Vec<TestPreview>,
    /// Said to run, but not by this host: remove, and report failed.
    orphaned: Vec<TestPreview>,
    /// Ending: remove, and say so.
    remove: Vec<TestPreview>,
}

fn plan(
    listed: &[TestPreview],
    started: &HashMap<String, Option<PathBuf>>,
    busy: &HashSet<String>,
) -> Plan {
    let mut plan = Plan::default();
    for preview in listed {
        if !valid(preview) || busy.contains(&preview.id) {
            continue;
        }
        match preview.status.as_str() {
            "pending" => plan.start.push(preview.clone()),
            "starting" | "ready" if !started.contains_key(&preview.id) => {
                plan.orphaned.push(preview.clone())
            }
            "ending" => plan.remove.push(preview.clone()),
            _ => {}
        }
    }
    plan
}

fn valid(preview: &TestPreview) -> bool {
    safe_id(&preview.id)
        && preview::validate_identifier("project ID", &preview.project_id).is_ok()
        && agents::valid_id(&preview.agent)
}

/// Looks at the coordinator's test previews once, and starts the work they
/// call for on threads of their own. `ready` waits until leftovers of an
/// earlier run are gone.
pub(crate) async fn look(client: &reqwest::Client, ready: impl Fn() + Clone + Send + 'static) {
    let Ok(response) = client
        .get(preview::coordinator_url("/api/v1/test-previews"))
        .send()
        .await
    else {
        return;
    };
    let Ok(listed) = preview::coordinator_json::<TestPreviews>(response, "test previews").await
    else {
        return;
    };
    let listed = listed.previews;
    let (plan, sweep) = with_owned(|owned| {
        let plan = plan(&listed, &owned.started, &owned.busy);
        for preview in plan.start.iter().chain(&plan.orphaned).chain(&plan.remove) {
            owned.busy.insert(preview.id.clone());
        }
        let sweep = owned.swept.is_none_or(|at| at.elapsed() >= SWEEP_EVERY);
        if sweep {
            owned.swept = Some(Instant::now());
        }
        (plan, sweep)
    });
    for preview in plan.start {
        let ready = ready.clone();
        std::thread::spawn(move || {
            ready();
            crate::runtime::block_on(claim_and_start(preview));
        });
    }
    for preview in plan.orphaned {
        std::thread::spawn(move || {
            remove(&preview);
            let _ = crate::runtime::block_on(report_failed(
                &reqwest::Client::new(),
                &preview.id,
                "Commitarium's host restarted, which stopped the test copy of the app",
            ));
            with_owned(|owned| owned.busy.remove(&preview.id));
        });
    }
    for preview in plan.remove {
        std::thread::spawn(move || {
            remove(&preview);
            let said = crate::runtime::block_on(post(
                &reqwest::Client::new(),
                &format!("/api/v1/test-previews/{}/removed", preview.id),
                None::<&()>,
            ));
            with_owned(|owned| {
                owned.busy.remove(&preview.id);
                if said.is_ok() {
                    owned.started.remove(&preview.id);
                }
            });
        });
    }
    if sweep {
        let wanted: HashSet<String> = listed
            .iter()
            .filter(|preview| matches!(preview.status.as_str(), "pending" | "starting" | "ready"))
            .map(|preview| preview.id.clone())
            .collect();
        std::thread::spawn(move || sweep_unwanted(&wanted));
    }
}

async fn claim_and_start(preview: TestPreview) {
    let client = reqwest::Client::new();
    let claimed = post(
        &client,
        &format!("/api/v1/test-previews/{}/claim", preview.id),
        None::<&()>,
    )
    .await;
    if claimed.is_err() {
        // Someone else claimed it, or it ended meanwhile.
        with_owned(|owned| owned.busy.remove(&preview.id));
        return;
    }
    with_owned(|owned| owned.started.insert(preview.id.clone(), None));
    let started = start(&client, &preview).await;
    let reported = match &started {
        Ok(address) => {
            post(
                &client,
                &format!("/api/v1/test-previews/{}/ready", preview.id),
                Some(&Ready { address }),
            )
            .await
        }
        Err(reason) => {
            eprintln!("[test preview] {} did not start: {reason}", preview.id);
            remove(&preview);
            report_failed(&client, &preview.id, reason).await
        }
    };
    if started.is_ok() && reported.is_err() {
        // The coordinator will not hear of it: a look at its list removes
        // it once it ends there.
        eprintln!(
            "[test preview] {} runs, but the coordinator was not told",
            preview.id
        );
    }
    with_owned(|owned| owned.busy.remove(&preview.id));
}

/// Starts the test preview and connects the worker; answers its address.
async fn start(client: &reqwest::Client, preview: &TestPreview) -> Result<String, String> {
    let manifest: ToolchainManifest = preview::coordinator_json(
        client
            .get(preview::coordinator_url(&format!(
                "/api/v1/projects/{}/toolchain",
                preview.project_id
            )))
            .send()
            .await
            .map_err(|e| format!("request project stack from coordinator: {e}"))?,
        "project stack",
    )
    .await?;
    if manifest.status != "configured" {
        return Err("the project has no stack yet, so its app cannot run".into());
    }
    if let Some(run) = &manifest.run {
        preview::validate_run(run)?;
    }
    let handoff: ProjectHandoff = preview::coordinator_json(
        client
            .get(preview::coordinator_url(&format!(
                "/api/v1/projects/{}/handoff",
                preview.project_id
            )))
            .send()
            .await
            .map_err(|e| format!("request project handoff from coordinator: {e}"))?,
        "project handoff",
    )
    .await?;
    preview::validate_handoff(&handoff, &preview.project_id)?;
    let accounts = match crate::test_accounts::fetch(client, &preview.project_id).await {
        Ok(accounts) => Some(accounts),
        Err(error) => {
            eprintln!("[test preview] starting without test accounts: {error}");
            None
        }
    };
    let preview = preview.clone();
    let open = manifest.run.map(|run| run.open);
    crate::runtime::spawn_blocking(move || launch(&preview, &handoff, open, accounts))
        .await
        .map_err(|_| "the test preview task stopped unexpectedly".to_string())?
}

fn launch(
    preview: &TestPreview,
    handoff: &ProjectHandoff,
    open: Option<RunOpen>,
    accounts: Option<crate::test_accounts::TestAccounts>,
) -> Result<String, String> {
    let root =
        preview::prepare_commit_source(&handoff.source.repository, &handoff.source.head_commit_id)?;
    with_owned(|owned| owned.started.insert(preview.id.clone(), Some(root.clone())));
    let source = root.join("source");
    let accounts_file = accounts
        .as_ref()
        .map(|accounts| crate::test_accounts::write(accounts, &root))
        .transpose()?;
    let compose_id = compose_id(&preview.project_id, &preview.id);
    let configured = open.map(|open| preview_compose::OpenTarget {
        service: open.service,
        port: open.port,
    });
    let prepared = preview_compose::normalize_and_rewrite(
        &source,
        &compose_id,
        configured.as_ref(),
        accounts_file.as_deref(),
    )?;
    let runtime = ComposeRuntime {
        project_name: prepared.project_name.clone(),
        compose_file: root.join("test.compose.json"),
        environment_file: root.join("test.env"),
        source_root: root.clone(),
    };
    preview_compose::write_rewritten(&prepared, &runtime.compose_file)?;
    fs::write(&runtime.environment_file, [])
        .map_err(|e| format!("write isolated preview environment file: {e}"))?;
    let open = RunOpen {
        service: prepared.open.service.clone(),
        port: prepared.open.port,
    };
    tear_down(&runtime.project_name);
    crate::preview_check::start_and_watch(&runtime, &prepared.published_ports, &open)
        .map_err(|failure| failure.reason())?;
    reach(&runtime.project_name, &open, &preview.agent)
}

/// Connects the agent's worker to the network the open service is on, and
/// answers the address it reaches the service at.
fn reach(project_name: &str, open: &RunOpen, agent: &str) -> Result<String, String> {
    let service = labeled_containers(&[
        (COMPOSE_PROJECT_LABEL, project_name),
        (COMPOSE_SERVICE_LABEL, &open.service),
    ])?
    .into_iter()
    .next()
    .ok_or("the service the preview opens is not running")?;
    let own_networks = labeled("network", project_name)?;
    let networks = docker_output(
        docker::docker_command()
            .args(["inspect", "--format", "{{json .NetworkSettings.Networks}}"])
            .arg(&service),
        "inspect the app's networks",
    )?;
    let (network, ip) = choose_network(&networks, &own_networks)?;
    let worker = labeled_containers(&[
        (COMPOSE_PROJECT_LABEL, docker::PROJECT_NAME),
        (COMPOSE_SERVICE_LABEL, &agents::worker_service(agent)),
    ])?
    .into_iter()
    .next()
    .ok_or("the helper's agent is not running")?;
    let connected = docker::docker_command()
        .args(["network", "connect", &network, &worker])
        .output()
        .map_err(|e| format!("connect the helper to the app: {e}"))?;
    let stderr = String::from_utf8_lossy(&connected.stderr);
    if !connected.status.success() && !stderr.contains("already exists") {
        return Err(format!(
            "connect the helper to the app: {}",
            preview::bounded(stderr.trim())
        ));
    }
    let address = format!("http://{ip}:{}", open.port);
    // The worker should now reach it; curl is in every worker image, and
    // any HTTP answer will do.
    let deadline = Instant::now() + REACH_TIMEOUT;
    loop {
        let reached = docker::docker_command()
            .args([
                "exec",
                &worker,
                "curl",
                "-sS",
                "-o",
                "/dev/null",
                "--max-time",
                "5",
            ])
            .arg(&address)
            .output()
            .is_ok_and(|output| output.status.success());
        if reached {
            return Ok(address);
        }
        if Instant::now() >= deadline {
            return Err("the helper's agent could not reach the test copy of the app".into());
        }
        std::thread::sleep(Duration::from_secs(2));
    }
}

/// The open service's network to connect the worker to, and its address
/// there: one of the test preview's own networks, the first by name.
fn choose_network(inspected: &str, own: &[String]) -> Result<(String, Ipv4Addr), String> {
    #[derive(Deserialize)]
    struct Endpoint {
        #[serde(rename = "IPAddress", default)]
        ip_address: String,
    }
    let networks: BTreeMap<String, Option<Endpoint>> = serde_json::from_str(inspected.trim())
        .map_err(|_| "Docker described the app's networks in a way Commitarium cannot read")?;
    networks
        .into_iter()
        .filter(|(name, _)| own.contains(name))
        .find_map(|(name, endpoint)| {
            let ip: Ipv4Addr = endpoint?.ip_address.parse().ok()?;
            ip.is_private().then_some((name, ip))
        })
        .ok_or_else(|| {
            "the service the preview opens has no network of its own to reach it on".into()
        })
}

/// Removes everything a test preview created: first the worker's reach,
/// then its containers, networks, data, images and sources.
fn remove(preview: &TestPreview) {
    if let Ok(project) =
        preview_compose::project_name(&compose_id(&preview.project_id, &preview.id))
    {
        tear_down(&project);
    }
    if let Some(Some(root)) = with_owned(|owned| owned.started.get(&preview.id).cloned()) {
        preview::remove_source(&root);
    }
}

/// Disconnects every container that is not the test preview's own from its
/// networks, then removes what it created. Safe to run again, and on what
/// is already gone.
pub(crate) fn tear_down(project: &str) {
    for network in labeled("network", project).unwrap_or_default() {
        let Ok(members) = docker_output(
            docker::docker_command().args([
                "network",
                "inspect",
                "--format",
                "{{range $id, $c := .Containers}}{{$id}} {{end}}",
                &network,
            ]),
            "list the app network's containers",
        ) else {
            continue;
        };
        for container in members.split_whitespace() {
            let owner = docker_output(
                docker::docker_command().args([
                    "inspect",
                    "--format",
                    &format!("{{{{index .Config.Labels \"{COMPOSE_PROJECT_LABEL}\"}}}}"),
                    container,
                ]),
                "inspect a container on the app's network",
            )
            .unwrap_or_default();
            if foreign(owner.trim(), project) {
                let _ = docker::docker_command()
                    .args(["network", "disconnect", "--force", &network, container])
                    .output();
            }
        }
    }
    for kind in ["container", "network", "volume", "image"] {
        preview::remove_labeled_resources(kind, project);
    }
}

/// Whether a container on a test preview's network is someone else's: a
/// worker the helper ran on.
fn foreign(owner: &str, project: &str) -> bool {
    owner != project
}

/// Removes the test previews whose compose projects nobody wants any more:
/// those a host that stopped left, or whose records went.
fn sweep_unwanted(wanted: &HashSet<String>) {
    let busy = with_owned(|owned| owned.busy.clone());
    for project in test_projects() {
        let Some(id) = test_preview_of(&project) else {
            continue;
        };
        if !wanted.contains(id) && !busy.contains(id) {
            tear_down(&project);
        }
    }
}

/// The compose projects of test previews that own containers, networks or
/// volumes.
pub(crate) fn test_projects() -> HashSet<String> {
    let mut projects = HashSet::new();
    for list in [
        &["ps", "-a"][..],
        &["network", "ls"][..],
        &["volume", "ls"][..],
    ] {
        if let Ok(output) = docker_output(
            docker::docker_command()
                .args(list)
                .args(["--filter", &format!("label={COMPOSE_PROJECT_LABEL}")])
                .args([
                    "--format",
                    &format!("{{{{.Label \"{COMPOSE_PROJECT_LABEL}\"}}}}"),
                ]),
            "list test previews",
        ) {
            projects.extend(
                output
                    .lines()
                    .map(str::trim)
                    .filter(|name| is_test_project(name))
                    .map(str::to_string),
            );
        }
    }
    projects
}

fn labeled(kind: &str, project: &str) -> Result<Vec<String>, String> {
    let output = docker_output(
        docker::docker_command().args([
            kind,
            "ls",
            "--filter",
            &format!("label={COMPOSE_PROJECT_LABEL}={project}"),
            "--format",
            "{{.Name}}",
        ]),
        "list the test preview's resources",
    )?;
    Ok(output
        .lines()
        .map(str::trim)
        .filter(|line| !line.is_empty())
        .map(str::to_string)
        .collect())
}

fn labeled_containers(labels: &[(&str, &str)]) -> Result<Vec<String>, String> {
    let mut command = docker::docker_command();
    command.args(["ps", "-q"]);
    for (key, value) in labels {
        command.args(["--filter", &format!("label={key}={value}")]);
    }
    let output = docker_output(&mut command, "find a container")?;
    Ok(output.split_whitespace().map(str::to_string).collect())
}

fn docker_output(command: &mut std::process::Command, action: &str) -> Result<String, String> {
    let output = command.output().map_err(|e| format!("{action}: {e}"))?;
    if !output.status.success() {
        return Err(format!(
            "{action}: {}",
            preview::bounded(String::from_utf8_lossy(&output.stderr).trim())
        ));
    }
    Ok(String::from_utf8_lossy(&output.stdout).into_owned())
}

async fn report_failed(client: &reqwest::Client, id: &str, reason: &str) -> Result<(), String> {
    post(
        client,
        &format!("/api/v1/test-previews/{id}/failed"),
        Some(&Failed {
            reason: &preview::bounded(reason),
        }),
    )
    .await
}

async fn post<T: Serialize>(
    client: &reqwest::Client,
    path: &str,
    body: Option<&T>,
) -> Result<(), String> {
    let request = client.post(preview::coordinator_url(path));
    let request = match body {
        Some(body) => request.json(body),
        None => request.body(Vec::new()),
    };
    let response = request.send().await.map_err(|e| format!("{path}: {e}"))?;
    if response.status().is_success() {
        Ok(())
    } else {
        Err(format!("{path}: HTTP {}", response.status()))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const ID: &str = "tsk_0123456789abcdef01234567";

    fn listed(id: &str, status: &str) -> TestPreview {
        TestPreview {
            id: id.into(),
            project_id: "prj_abc".into(),
            agent: "claude".into(),
            status: status.into(),
        }
    }

    #[test]
    fn helper_ids_and_their_compose_projects_are_recognized() {
        assert!(safe_id(ID));
        for bad in [
            "tsk_0123",
            "tsk_0123456789ABCDEF01234567",
            "fea_0123456789abcdef01234567",
            "tsk_0123456789abcdef0123456/",
        ] {
            assert!(!safe_id(bad), "{bad}");
        }
        let project = preview_compose::project_name(&compose_id("prj_abc", ID)).unwrap();
        assert_eq!(project, format!("commitarium-preview-prj_abc-{ID}"));
        assert_eq!(test_preview_of(&project), Some(ID));
        for other in [
            "commitarium-preview-prj_abc",
            "commitarium-preview-prj_abc-fea_q2",
            "commitarium-preview-prj_abc-check",
            "commitarium-preview-prj_abc-live",
            "commitarium",
            &format!("other-prj_abc-{ID}"),
        ] {
            assert!(!is_test_project(other), "{other}");
        }
    }

    #[test]
    fn a_look_starts_pending_ones_removes_ending_ones_and_fails_ones_it_did_not_start() {
        let mut started = HashMap::new();
        started.insert("tsk_1123456789abcdef01234567".to_string(), None);
        let busy: HashSet<String> = ["tsk_4123456789abcdef01234567".to_string()].into();
        let list = [
            listed(ID, "pending"),
            listed("tsk_1123456789abcdef01234567", "ready"),
            listed("tsk_2123456789abcdef01234567", "ready"),
            listed("tsk_3123456789abcdef01234567", "ending"),
            listed("tsk_4123456789abcdef01234567", "ending"),
            listed("tsk_5123456789abcdef01234567", "failed"),
            TestPreview {
                agent: "Bad Agent".into(),
                ..listed("tsk_6123456789abcdef01234567", "pending")
            },
            TestPreview {
                project_id: "../x".into(),
                ..listed("tsk_7123456789abcdef01234567", "ending")
            },
        ];
        let plan = plan(&list, &started, &busy);
        let ids =
            |previews: &[TestPreview]| previews.iter().map(|p| p.id.clone()).collect::<Vec<_>>();
        assert_eq!(ids(&plan.start), [ID]);
        assert_eq!(ids(&plan.orphaned), ["tsk_2123456789abcdef01234567"]);
        assert_eq!(ids(&plan.remove), ["tsk_3123456789abcdef01234567"]);
    }

    #[test]
    fn the_worker_joins_the_open_services_own_network() {
        let inspected = r#"{
            "commitarium-preview-prj_abc-tsk_x.backend": {"IPAddress": "172.21.0.3"},
            "commitarium-preview-prj_abc-tsk_x.default": {"IPAddress": "172.20.0.4"},
            "bridge": {"IPAddress": "172.17.0.2"}
        }"#;
        let own = [
            "commitarium-preview-prj_abc-tsk_x.default".to_string(),
            "commitarium-preview-prj_abc-tsk_x.backend".to_string(),
        ];
        let (network, ip) = choose_network(inspected, &own).unwrap();
        assert_eq!(network, "commitarium-preview-prj_abc-tsk_x.backend");
        assert_eq!(ip, Ipv4Addr::new(172, 21, 0, 3));
        // Never a network the preview does not own, such as Docker's bridge.
        assert!(choose_network(r#"{"bridge": {"IPAddress": "172.17.0.2"}}"#, &own).is_err());
        assert!(choose_network(
            r#"{"commitarium-preview-prj_abc-tsk_x.default": {"IPAddress": ""}}"#,
            &own
        )
        .is_err());
        assert!(choose_network("not json", &own).is_err());
    }

    #[test]
    fn only_containers_of_other_projects_are_disconnected() {
        let project = format!("commitarium-preview-prj_abc-{ID}");
        assert!(!foreign(&project, &project));
        assert!(foreign("commitarium", &project));
        assert!(foreign("", &project));
    }

    /// Needs Docker and the nginx:stable-alpine image: a worker left on a
    /// test preview's network is disconnected, then everything goes.
    #[test]
    #[ignore = "needs Docker"]
    fn live_tear_down_disconnects_a_worker_and_removes_the_rest() {
        let project = format!("h6probe-{ID}");
        let network = format!("{project}.default");
        let label = format!("{COMPOSE_PROJECT_LABEL}={project}");
        let run = |args: &[&str]| {
            let output = docker::docker_command().args(args).output().unwrap();
            assert!(
                output.status.success(),
                "{args:?}: {}",
                String::from_utf8_lossy(&output.stderr)
            );
        };
        run(&["network", "create", "--label", &label, &network]);
        run(&[
            "run",
            "-d",
            "--name",
            "h6probe-web",
            "--label",
            &label,
            "--network",
            &network,
            "nginx:stable-alpine",
        ]);
        run(&[
            "run",
            "-d",
            "--name",
            "h6probe-worker",
            "--network",
            &network,
            "nginx:stable-alpine",
        ]);
        tear_down(&project);
        assert!(labeled("network", &project).unwrap().is_empty());
        assert!(labeled_containers(&[(COMPOSE_PROJECT_LABEL, &project)])
            .unwrap()
            .is_empty());
        // The worker is not the test preview's: it stays, off the network.
        run(&["inspect", "h6probe-worker"]);
        run(&["rm", "--force", "h6probe-worker"]);
    }
}
