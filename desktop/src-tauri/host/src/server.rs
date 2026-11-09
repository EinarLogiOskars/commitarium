//! The host's HTTP API, on loopback. A port here is open to every local
//! process and any page that can reach it, so every route sits behind one
//! check (`Access::allows`): the exact Host, a known Origin, and the launch's
//! token. No route answers without it, no body is read before it, and no CORS
//! header is ever sent.
//!
//! Bodies can hold passwords and API keys, so they are never logged, and a
//! body that cannot be read is refused without repeating any of it. An error
//! answers `{"error": message}`, with the work's own message unchanged.

use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use std::io::{Cursor, Read, Write};
use std::path::PathBuf;
use std::sync::mpsc::RecvTimeoutError;
use std::sync::Arc;
use std::thread;
use std::time::Duration;
use tiny_http::{Header, Method, Request, Response, Server};
use tokio::sync::RwLock;
use zeroize::Zeroizing;

use crate::cli_updates::{self, CliUpdater};
use crate::deployments::Deployments;
use crate::events::{Bus, Events};
use crate::preview::{self, PreviewManager, PreviewTarget};
use crate::profiles::{self, ProfileManager};
use crate::publishing::{Access as PublishAccess, Publishing};
use crate::runtime::block_on;
use crate::server_backups::{ServerBackups, Stack};
use crate::{bootstrap, disk, docker, environment, preview_check};

/// How often a quiet stream proves it is alive, and notices a client has gone.
const KEEP_ALIVE: Duration = Duration::from_secs(15);
/// Raised when an app and a host of different versions can no longer work
/// together. A client that does not match still reaches `/v1/host/update`.
pub const API_VERSION: u32 = 1;
/// How long a service waits before starting the stack again.
const STACK_RETRY: Duration = Duration::from_secs(30);
/// More than any request needs: the longest is a project's open sessions.
pub const MAX_BODY: usize = 1024 * 1024;

/// What the host is told at launch, over stdin, so it never shows in a
/// process listing.
#[derive(Deserialize, Serialize)]
pub struct Launch {
    pub token: String,
    pub origins: Vec<String>,
    pub data_dir: PathBuf,
}

/// The host's first and only line on stdout: where it listens and what it is.
#[derive(Debug, Deserialize, PartialEq, Serialize)]
pub struct Ready {
    pub port: u16,
    pub version: String,
}

/// What every request can reach: all the host's state. Only this process
/// holds it, so a request from any client sees the same logins and previews.
pub struct Shared {
    data_dir: PathBuf,
    bus: Bus,
    events: Events,
    profiles: ProfileManager,
    previews: PreviewManager,
    deployments: Deployments,
    publishing: Publishing,
    backups: ServerBackups,
    updater: Arc<CliUpdater>,
    /// Held for writing until leftovers are swept, because a sweep removes
    /// any preview, and one started during it would be taken for a leftover.
    swept: Arc<RwLock<()>>,
    /// The gateway and its port, on a headless host only.
    #[cfg(unix)]
    gateway: std::sync::OnceLock<(Arc<crate::gateway::Gateway>, u16)>,
}

impl Shared {
    pub fn new(data_dir: PathBuf, bus: Bus) -> Self {
        Self {
            deployments: Deployments::new(&data_dir),
            publishing: Publishing::new(&data_dir),
            backups: ServerBackups::new(&data_dir),
            data_dir,
            events: Events::new(bus.clone()),
            bus,
            profiles: ProfileManager::new(),
            previews: PreviewManager::default(),
            updater: Arc::default(),
            swept: Arc::default(),
            #[cfg(unix)]
            gateway: std::sync::OnceLock::new(),
        }
    }

    #[cfg(unix)]
    pub fn previews(&self) -> PreviewManager {
        self.previews.clone()
    }

    #[cfg(unix)]
    pub fn deployments(&self) -> Deployments {
        self.deployments.clone()
    }

    /// Starts again what was deployed, once leftovers are swept. Only a
    /// server keeps its deployments: an app on this computer has none.
    #[cfg(unix)]
    pub fn restore_deployments(&self) {
        self.wait_for_sweep();
        block_on(self.deployments.restore(self.events.clone()));
    }

    /// Keeps Caddy in step with what is published, on a server.
    #[cfg(unix)]
    pub fn keep_publishing(&self) {
        self.publishing
            .clone()
            .keep_in_sync(self.deployments.clone());
        crate::apps_page::serve(self.publishing.clone(), self.deployments.clone());
    }

    /// A backup each night, on a server: the app on this computer backs up
    /// when its user asks.
    #[cfg(unix)]
    pub fn keep_nightly_backups(self: &Arc<Self>) {
        let shared = self.clone();
        self.backups.clone().keep_nightly(move |backups| {
            if let Err(error) = shared.backed_up(backups.create(&shared.stack())) {
                eprintln!("[backups] the nightly backup failed: {error}");
            }
        });
    }

    /// A backup or restore stops the stack, and deployed apps with it: they
    /// start again from what was deployed, as when the host starts.
    fn backed_up<T>(&self, result: Result<T, String>) -> Result<T, String> {
        #[cfg(unix)]
        self.restore_deployments();
        result
    }

    fn stack(&self) -> Stack<'_> {
        Stack {
            events: self.events.clone(),
            profiles: &self.profiles,
            updater: &self.updater,
        }
    }

    /// Publishing is for a server: an app on this computer opens in windows.
    fn on_a_server(&self) -> Result<(), String> {
        #[cfg(unix)]
        if self.gateway.get().is_some() {
            return Ok(());
        }
        Err("publishing works on a server only".to_string())
    }

    #[cfg(unix)]
    pub fn use_gateway(&self, gateway: Arc<crate::gateway::Gateway>, port: u16) {
        let _ = self.gateway.set((gateway, port));
    }

    #[cfg(unix)]
    fn gateway(&self) -> Result<&(Arc<crate::gateway::Gateway>, u16), String> {
        self.gateway
            .get()
            .ok_or_else(|| "this host takes no remote devices".to_string())
    }

    /// Clears what an earlier host left and starts the agents' CLI updates.
    /// This uses the real Docker, and never holds up the host's start. Call it
    /// once, before serving: no preview runs yet in a new host, so whatever is
    /// left is from an earlier one.
    pub fn begin_background_work(&self) {
        let Ok(sweeping) = self.swept.clone().try_write_owned() else {
            return;
        };
        let updater = self.updater.clone();
        let profiles = self.profiles.clone();
        let events = self.events.clone();
        thread::spawn(move || {
            preview::sweep_leftovers();
            profiles::sweep_login_containers();
            drop(sweeping);
            cli_updates::spawn(updater, profiles, events);
        });
    }

    /// Brings the stack up, for a host no app starts. Docker can come up after
    /// the service does, so a failure is tried again until it works.
    pub fn keep_trying_to_start_stack(&self) {
        loop {
            match block_on(docker::stack_start(
                self.events.clone(),
                &self.profiles,
                &self.updater,
            )) {
                Ok(_) => return,
                Err(error) => eprintln!("[host] the stack did not start: {error}"),
            }
            thread::sleep(STACK_RETRY);
        }
    }

    pub(crate) fn wait_for_sweep(&self) {
        drop(block_on(self.swept.read()));
    }

    /// Stops what would otherwise be orphaned when the app is gone, and the
    /// deployments: on this computer the real apps stop with Commitarium, and
    /// a server starts its own again when its host starts. Their data stays.
    /// The stack is the user's to stop, so it stays up.
    pub fn stop_previews_and_logins(&self) {
        self.deployments.stop_all();
        preview::stop_all(&self.previews);
        self.profiles.shutdown();
    }
}

pub struct Access {
    token: String,
    origins: Vec<String>,
    host: String,
}

impl Access {
    pub fn new(launch: Launch, port: u16) -> Self {
        Self {
            token: launch.token,
            origins: launch.origins,
            host: format!("127.0.0.1:{port}"),
        }
    }

    /// `host`, `origin`, and `authorization` are the request's headers. A
    /// program sends no Origin; a page cannot leave it out, so one from
    /// elsewhere is refused even with the right token.
    pub fn allows(
        &self,
        host: Option<&str>,
        origin: Option<&str>,
        authorization: Option<&str>,
    ) -> bool {
        let known_host = host == Some(self.host.as_str());
        let known_origin = origin.is_none_or(|origin| self.origins.iter().any(|o| o == origin));
        let token = authorization
            .and_then(|value| value.strip_prefix("Bearer "))
            .unwrap_or("");
        !self.token.is_empty()
            && known_host
            && known_origin
            && same(token.as_bytes(), self.token.as_bytes())
    }
}

/// Compares without stopping at the first difference.
fn same(a: &[u8], b: &[u8]) -> bool {
    a.len() == b.len() && a.iter().zip(b).fold(0, |diff, (x, y)| diff | (x ^ y)) == 0
}

fn header<'a>(request: &'a Request, name: &'static str) -> Option<&'a str> {
    request
        .headers()
        .iter()
        .find(|header| header.field.equiv(name))
        .map(|header| header.value.as_str())
}

type Reply = Response<Cursor<Vec<u8>>>;

fn json(status: u16, body: String) -> Reply {
    let content_type = Header::from_bytes("Content-Type", "application/json")
        .expect("the content type is a valid header");
    Response::from_string(body)
        .with_status_code(status)
        .with_header(content_type)
}

fn failure(status: u16, message: &str) -> Reply {
    json(status, serde_json::json!({ "error": message }).to_string())
}

fn respond_with<T: Serialize>(result: Result<T, String>) -> Reply {
    match result.and_then(|value| serde_json::to_string(&value).map_err(|e| e.to_string())) {
        Ok(body) => json(200, body),
        Err(error) => failure(500, &error),
    }
}

/// Runs `work` with the request's body. A parser's error can quote what it
/// read, so it stays here.
fn with_body<B: DeserializeOwned, T: Serialize>(
    body: &[u8],
    work: impl FnOnce(B) -> Result<T, String>,
) -> Reply {
    match serde_json::from_slice(body) {
        Ok(body) => respond_with(work(body)),
        Err(_) => failure(400, "the request body is not valid"),
    }
}

/// Runs `work` with the request's body, or the body's defaults when there is
/// none: for requests that once had no body, so older clients still work.
fn with_optional_body<B: DeserializeOwned + Default, T: Serialize>(
    body: &[u8],
    work: impl FnOnce(B) -> Result<T, String>,
) -> Reply {
    if body.iter().all(u8::is_ascii_whitespace) {
        return respond_with(work(B::default()));
    }
    match serde_json::from_slice::<Option<B>>(body) {
        Ok(body) => respond_with(work(body.unwrap_or_default())),
        Err(_) => failure(400, "the request body is not valid"),
    }
}

fn respond_to_get(path: &str, shared: &Shared) -> Reply {
    match path {
        "/v1/version" => json(
            200,
            serde_json::json!({
                "version": env!("CARGO_PKG_VERSION"),
                "api_version": API_VERSION,
            })
            .to_string(),
        ),
        "/v1/stack/status" => respond_with(docker::stack_status()),
        "/v1/docker/probe" => respond_with(Ok(docker::docker_probe())),
        "/v1/disk/space" => respond_with(disk::disk_space_probe(&shared.data_dir)),
        "/v1/profiles" => respond_with(block_on(profiles::list_profiles(&shared.profiles))),
        "/v1/deployments" => respond_with(shared.deployments.list()),
        "/v1/backups" => respond_with(shared.on_a_server().and_then(|()| shared.backups.list())),
        "/v1/backups/underway" => respond_with(Ok(
            serde_json::json!({ "underway": shared.backups.underway() }),
        )),
        "/v1/publishing" => respond_with(
            shared
                .on_a_server()
                .and_then(|()| shared.publishing.overview(&shared.deployments)),
        ),
        "/v1/agents/cli-status" => respond_with(Ok(cli_updates::agent_cli_status(&shared.updater))),
        "/v1/forgejo/viewer" => respond_with(block_on(bootstrap::get_forgejo_viewer_status())),
        #[cfg(unix)]
        "/v1/host/update" => respond_with(block_on(crate::host_update::check())),
        #[cfg(unix)]
        "/v1/devices" => respond_with(shared.gateway().map(|(gateway, _)| gateway.devices())),
        _ => failure(404, "not found"),
    }
}

#[derive(Deserialize)]
struct ProfileRequest {
    profile_id: String,
}

#[derive(Deserialize)]
struct BeginLogin {
    profile_id: String,
    method: String,
}

#[derive(Deserialize)]
struct LoginCode {
    profile_id: String,
    code: String,
}

#[derive(Deserialize)]
struct ApiKey {
    profile_id: String,
    key: String,
}

#[derive(Deserialize)]
struct ProjectRequest {
    project_id: String,
}

#[derive(Deserialize)]
struct RestoreBackup {
    name: String,
}

#[derive(Deserialize)]
struct SetDomain {
    domain: String,
    #[serde(default)]
    records: crate::publishing::Records,
}

/// Which of the server's domains; the first when left out.
#[derive(Default, Deserialize)]
struct DomainRequest {
    #[serde(default)]
    domain: Option<String>,
}

#[derive(Deserialize)]
struct SetPlacement {
    placement: crate::publishing::Placement,
}

#[derive(Deserialize)]
struct Publish {
    project_id: String,
    name: String,
    #[serde(default)]
    access: PublishAccess,
    /// A private app's password, when set or changed.
    #[serde(default)]
    password: Option<String>,
    /// Its domain, `www` and extra addresses; each kept as it was when left
    /// out.
    #[serde(flatten)]
    placing: crate::publishing::Placing,
}

#[derive(Deserialize)]
struct SetAccess {
    project_id: String,
    access: PublishAccess,
}

/// The apps page: who may open it, or `None` to turn it off.
#[derive(Deserialize)]
struct SetAppsPage {
    access: Option<PublishAccess>,
    password: Option<String>,
    /// Its address name; `None` keeps the one it has, or `apps`.
    #[serde(default)]
    name: Option<String>,
    /// Whose page; the first domain's when left out.
    #[serde(default)]
    domain: Option<String>,
}

#[derive(Deserialize)]
struct AppsPageName {
    name: String,
    #[serde(default)]
    domain: Option<String>,
}

#[derive(Deserialize)]
struct SetReach {
    reach: crate::publishing::Reach,
}

#[derive(Deserialize)]
struct SetListed {
    project_id: String,
    listed: bool,
}

#[derive(Deserialize)]
struct SetGroups {
    project_id: String,
    groups: Vec<String>,
}

#[derive(Deserialize)]
struct SetPassword {
    project_id: String,
    password: String,
}

#[derive(Deserialize)]
struct Deploy {
    project_id: String,
    /// The head of the default branch when absent.
    #[serde(default)]
    commit_id: Option<String>,
}

#[derive(Deserialize)]
struct StartPreview {
    project_id: String,
    target: PreviewTarget,
}

#[derive(Deserialize)]
struct PreviewRequest {
    project_id: String,
    feature_id: Option<String>,
}

#[derive(Deserialize)]
struct PrunePreviews {
    project_id: String,
    open_sessions: Vec<String>,
}

#[derive(Deserialize)]
struct EnvironmentRequest {
    request_id: String,
}

#[derive(Deserialize)]
struct ValidationJob {
    job_id: String,
}

#[derive(Deserialize)]
struct PreviewCheck {
    check_id: String,
}

#[cfg(unix)]
#[derive(Deserialize)]
struct RevokeDevice {
    device: String,
}

#[derive(Deserialize)]
struct ViewerPassword {
    password: String,
}

fn respond_to_post(path: &str, body: &[u8], shared: &Shared) -> Reply {
    let events = || shared.events.clone();
    let logins = &shared.profiles;
    let previews = &shared.previews;
    match path {
        #[cfg(unix)]
        "/v1/host/update" => respond_with(
            shared
                .gateway()
                .and_then(|_| block_on(crate::host_update::install())),
        ),
        #[cfg(unix)]
        "/v1/pairing/open" => respond_with(
            shared
                .gateway()
                .and_then(|(gateway, port)| gateway.open_pairing(*port)),
        ),
        #[cfg(unix)]
        "/v1/devices/revoke" => with_body(body, |revoke: RevokeDevice| {
            shared
                .gateway()
                .and_then(|(gateway, _)| gateway.revoke(&revoke.device))
        }),
        "/v1/stack/start" => respond_with(block_on(docker::stack_start(
            events(),
            logins,
            &shared.updater,
        ))),
        "/v1/stack/up" => respond_with(block_on(docker::stack_up(
            events(),
            logins,
            &shared.updater,
        ))),
        "/v1/stack/down" => respond_with(block_on(docker::stack_down())),
        "/v1/stack/update" => respond_with(block_on(docker::stack_update(
            events(),
            logins,
            &shared.updater,
        ))),

        "/v1/profiles/login/begin" => with_body(body, |login: BeginLogin| {
            profiles::begin_login(events(), logins, login.profile_id, login.method)
        }),
        "/v1/profiles/login/code" => with_body(body, |login: LoginCode| {
            profiles::submit_login_code(logins, login.profile_id, login.code)
        }),
        "/v1/profiles/login/key" => with_body(body, |login: ApiKey| {
            profiles::submit_api_key(events(), logins, login.profile_id, login.key)
        }),
        "/v1/profiles/login/cancel" => with_body(body, |login: ProfileRequest| {
            profiles::cancel_login(events(), logins, login.profile_id)
        }),
        "/v1/profiles/verify" => with_body(body, |profile: ProfileRequest| {
            block_on(profiles::verify_profile(logins, profile.profile_id))
        }),
        "/v1/profiles/disconnect" => with_body(body, |profile: ProfileRequest| {
            block_on(profiles::disconnect_profile(logins, profile.profile_id))
        }),
        "/v1/profiles/shutdown" => {
            logins.shutdown();
            respond_with(Ok(()))
        }

        "/v1/deployments/deploy" => with_body(body, |deploy: Deploy| {
            shared.wait_for_sweep();
            block_on(
                shared
                    .deployments
                    .deploy(events(), deploy.project_id, deploy.commit_id),
            )
        }),
        "/v1/deployments/stop" => with_body(body, |stop: ProjectRequest| {
            block_on(shared.deployments.stop(events(), stop.project_id))
        }),
        // The real app on this computer: the deployment, kept on the default
        // branch's head (HELPERS_PLAN.md, H7).
        "/v1/deployments/follow" => with_body(body, |follow: ProjectRequest| {
            shared.wait_for_sweep();
            block_on(shared.deployments.follow_head(events(), follow.project_id))
        }),
        "/v1/backups/create" => respond_with(
            shared
                .on_a_server()
                .and_then(|()| shared.backed_up(shared.backups.create(&shared.stack()))),
        ),
        "/v1/backups/restore" => with_body(body, |asked: RestoreBackup| {
            shared.on_a_server()?;
            shared.backed_up(shared.backups.restore(&shared.stack(), &asked.name))
        }),
        "/v1/publishing/domain" => with_body(body, |set: SetDomain| {
            shared.on_a_server()?;
            shared.publishing.set_domain(&set.domain, set.records)
        }),
        "/v1/publishing/add-domain" => with_body(body, |set: SetDomain| {
            shared.on_a_server()?;
            shared.publishing.add_domain(&set.domain, set.records)
        }),
        "/v1/publishing/remove-domain" => with_optional_body(body, |asked: DomainRequest| {
            shared.on_a_server()?;
            shared.publishing.remove_domain(asked.domain.as_deref())
        }),
        "/v1/publishing/placement" => with_body(body, |set: SetPlacement| {
            shared.on_a_server()?;
            shared.publishing.set_placement(set.placement)
        }),
        "/v1/publishing/publish" => with_body(body, |publish: Publish| {
            shared.on_a_server()?;
            shared.publishing.publish(
                &publish.project_id,
                &publish.name,
                publish.access,
                publish.password.as_deref(),
                publish.placing,
            )
        }),
        "/v1/publishing/password" => with_body(body, |set: SetPassword| {
            shared.on_a_server()?;
            shared
                .publishing
                .set_app_password(&set.project_id, &set.password)
        }),
        "/v1/publishing/access" => with_body(body, |set: SetAccess| {
            shared.on_a_server()?;
            shared.publishing.set_access(&set.project_id, set.access)
        }),
        "/v1/publishing/groups" => with_body(body, |set: SetGroups| {
            shared.on_a_server()?;
            shared.publishing.set_groups(&set.project_id, &set.groups)
        }),
        "/v1/publishing/sign-in" => with_body(body, |asked: crate::sign_in::Connect| {
            shared.on_a_server()?;
            shared.publishing.connect_sign_in(asked)
        }),
        "/v1/publishing/authentik/install" => with_optional_body(body, |asked: DomainRequest| {
            shared.on_a_server()?;
            shared.publishing.install_authentik(asked.domain.as_deref())
        }),
        "/v1/publishing/authentik/password" => respond_with(
            shared
                .on_a_server()
                .and_then(|()| shared.publishing.authentik_password()),
        ),
        "/v1/publishing/sign-in/disconnect" => with_optional_body(body, |asked: DomainRequest| {
            shared.on_a_server()?;
            shared
                .publishing
                .disconnect_sign_in(asked.domain.as_deref())
        }),
        "/v1/publishing/reveal" => with_body(body, |asked: ProjectRequest| {
            shared.on_a_server()?;
            shared.publishing.app_password(&asked.project_id)
        }),
        "/v1/publishing/apps-page" => with_body(body, |set: SetAppsPage| {
            shared.on_a_server()?;
            shared.publishing.set_apps_page(
                set.domain.as_deref(),
                set.access,
                set.password.as_deref(),
                set.name.as_deref(),
            )
        }),
        "/v1/publishing/apps-page/check" => with_body(body, |asked: AppsPageName| {
            shared.on_a_server()?;
            shared
                .publishing
                .check_apps_page_name(asked.domain.as_deref(), &asked.name)
        }),
        "/v1/publishing/apps-page/reveal" => with_optional_body(body, |asked: DomainRequest| {
            shared.on_a_server()?;
            shared
                .publishing
                .apps_page_password(asked.domain.as_deref())
        }),
        "/v1/publishing/reach" => with_body(body, |set: SetReach| {
            shared.on_a_server()?;
            shared.publishing.set_reach(set.reach)
        }),
        "/v1/publishing/listed" => with_body(body, |set: SetListed| {
            shared.on_a_server()?;
            shared.publishing.set_listed(&set.project_id, set.listed)
        }),
        "/v1/publishing/unpublish" => with_body(body, |asked: ProjectRequest| {
            shared.on_a_server()?;
            shared.publishing.unpublish(&asked.project_id)
        }),
        "/v1/previews/start" => with_body(body, |start: StartPreview| {
            if matches!(start.target, PreviewTarget::Deployment { .. }) {
                return Err("deploy through /v1/deployments/deploy".to_string());
            }
            shared.wait_for_sweep();
            block_on(preview::start_preview(
                events(),
                previews,
                start.project_id,
                start.target,
            ))
        }),
        "/v1/previews/stop" => with_body(body, |stop: PreviewRequest| {
            block_on(preview::stop_preview(
                events(),
                previews,
                stop.project_id,
                stop.feature_id,
            ))
        }),
        "/v1/previews/reset" => with_body(body, |reset: ProjectRequest| {
            block_on(preview::reset_preview_data(
                events(),
                previews,
                reset.project_id,
            ))
        }),
        "/v1/previews/prune" => with_body(body, |prune: PrunePreviews| {
            block_on(preview::prune_session_previews(
                events(),
                previews,
                prune.project_id,
                prune.open_sessions,
            ))
        }),
        "/v1/previews/status" => with_body(body, |asked: PreviewRequest| {
            preview::get_preview_status(previews, asked.project_id, asked.feature_id)
        }),
        "/v1/previews/logs" => with_body(body, |asked: PreviewRequest| {
            block_on(preview::get_preview_logs(
                previews,
                asked.project_id,
                asked.feature_id,
            ))
        }),
        "/v1/previews/delete-project" => with_body(body, |project: ProjectRequest| {
            // Its deployment, the real app on this computer, goes with it.
            block_on(
                shared
                    .deployments
                    .forget(events(), project.project_id.clone()),
            )?;
            block_on(preview::delete_project_data(
                &events(),
                previews.clone(),
                &project.project_id,
            ))
        }),
        "/v1/previews/stop-all" => {
            preview::stop_all(previews);
            respond_with(Ok(()))
        }

        "/v1/environment/provision" => with_body(body, |request: EnvironmentRequest| {
            block_on(environment::provision_environment_request(
                request.request_id,
                logins,
            ))
        }),
        "/v1/validation/run" => with_body(body, |job: ValidationJob| {
            block_on(environment::run_validation_job(job.job_id))
        }),
        "/v1/preview-check/run" => with_body(body, |check: PreviewCheck| {
            shared.wait_for_sweep();
            block_on(preview_check::run_preview_check(check.check_id))
        }),

        "/v1/forgejo/viewer" => with_body(body, |viewer: ViewerPassword| {
            block_on(bootstrap::configure_forgejo_viewer(viewer.password))
        }),
        _ => failure(404, "not found"),
    }
}

/// Reads no more than `MAX_BODY`, whatever the request claims. `None` means
/// there was more. A key can pass through here, so the bytes do not stay.
fn read_body(request: &mut Request) -> Option<Zeroizing<Vec<u8>>> {
    let claimed = request.body_length().unwrap_or(0);
    if claimed > MAX_BODY {
        return None;
    }
    let mut body = Zeroizing::new(Vec::with_capacity(claimed));
    request
        .as_reader()
        .take(MAX_BODY as u64 + 1)
        .read_to_end(&mut body)
        .ok()?;
    (body.len() <= MAX_BODY).then_some(body)
}

/// Writes straight to the socket, because a response body is buffered and an
/// update must leave at once. Ends when the client goes.
fn stream_events(request: Request, events: &Bus) {
    let updates = events.subscribe();
    let mut client = request.into_writer();
    let start = "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\
                 Cache-Control: no-store\r\nConnection: close\r\n\r\n: connected\n\n";
    let mut sent = client
        .write_all(start.as_bytes())
        .and_then(|()| client.flush());
    while sent.is_ok() {
        let frame = match updates.recv_timeout(KEEP_ALIVE) {
            Ok(frame) => frame,
            Err(RecvTimeoutError::Timeout) => ": keep-alive\n\n".to_string(),
            Err(RecvTimeoutError::Disconnected) => return,
        };
        sent = client
            .write_all(frame.as_bytes())
            .and_then(|()| client.flush());
    }
}

fn answer(request: &mut Request, shared: &Shared) -> Reply {
    let path = request.url().to_string();
    match request.method() {
        Method::Get => respond_to_get(&path, shared),
        Method::Post => match read_body(request) {
            Some(body) => respond_to_post(&path, &body, shared),
            None => failure(413, "the request is too large"),
        },
        _ => failure(405, "method not allowed"),
    }
}

/// Serves requests, each on its own thread, because Docker can be slow and
/// some work takes minutes as one long request.
pub fn serve(server: Server, access: Access, shared: Arc<Shared>) {
    let access = Arc::new(access);
    for mut request in server.incoming_requests() {
        let access = access.clone();
        let shared = shared.clone();
        thread::spawn(move || {
            let allowed = access.allows(
                header(&request, "Host"),
                header(&request, "Origin"),
                header(&request, "Authorization"),
            );
            if !allowed {
                let _ = request.respond(failure(401, "unauthorized"));
                return;
            }
            if *request.method() == Method::Get && request.url() == "/v1/events" {
                stream_events(request, &shared.bus);
                return;
            }
            let response = answer(&mut request, &shared);
            let _ = request.respond(response);
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn access() -> Access {
        Access::new(
            Launch {
                token: "secret".into(),
                origins: vec!["tauri://localhost".into()],
                data_dir: PathBuf::new(),
            },
            4000,
        )
    }

    const HOST: Option<&str> = Some("127.0.0.1:4000");
    const BEARER: Option<&str> = Some("Bearer secret");

    #[test]
    fn the_app_with_its_token_is_let_in() {
        assert!(access().allows(HOST, Some("tauri://localhost"), BEARER));
        assert!(access().allows(HOST, None, BEARER));
    }

    #[test]
    fn a_missing_or_wrong_token_is_refused() {
        assert!(!access().allows(HOST, None, None));
        assert!(!access().allows(HOST, None, Some("Bearer wrong!")));
        assert!(!access().allows(HOST, None, Some("secret")));
    }

    #[test]
    fn another_origin_is_refused_even_with_the_token() {
        assert!(!access().allows(HOST, Some("http://127.0.0.1:5000"), BEARER));
        assert!(!access().allows(HOST, Some("null"), BEARER));
    }

    #[test]
    fn a_name_rebound_to_loopback_is_refused() {
        assert!(!access().allows(Some("evil.example:4000"), None, BEARER));
        assert!(!access().allows(None, None, BEARER));
    }

    #[test]
    fn an_unconfigured_token_lets_nobody_in() {
        let access = Access::new(
            Launch {
                token: String::new(),
                origins: vec![],
                data_dir: PathBuf::new(),
            },
            4000,
        );
        assert!(!access.allows(HOST, None, Some("Bearer ")));
    }
}
