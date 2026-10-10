//! Trusted host bootstrap for Commitarium-owned credentials.
//!
//! The frontend never sees this module. It creates internal transport tokens
//! before Compose evaluates bind mounts, then uses Forgejo's fixed admin CLI to
//! create the service/agent identities and their scoped tokens. Existing valid
//! files and users are adopted so repeated app starts are harmless.

use getrandom::fill as random_fill;
use reqwest::header::{HeaderMap, HeaderValue, AUTHORIZATION};
use reqwest::{Method, StatusCode, Url};
use serde::{Deserialize, Serialize};
use std::collections::HashSet;
use std::fs::{self, OpenOptions};
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::thread;
use std::time::Duration;
use zeroize::Zeroizing;

use crate::{agents, docker};

const FORGEJO_READY_ATTEMPTS: usize = 60;
const FORGEJO_READY_DELAY: Duration = Duration::from_millis(250);
const FORGEJO_REQUEST_TIMEOUT: Duration = Duration::from_secs(10);
const FORGEJO_VIEWER_USERNAME: &str = "commitarium-viewer";
const FORGEJO_VIEWER_EMAIL: &str = "audit-viewer@commitarium.local";
const FORGEJO_VIEWER_FULL_NAME: &str = "Commitarium Audit Viewer";
const FORGEJO_DEFAULT_PORT: u16 = 3001;
const FORGEJO_REPOSITORY_PAGE_SIZE: usize = 50;
const FORGEJO_MAX_REPOSITORY_PAGES: usize = 1_000;

struct SecretLocation {
    source_variable: &'static str,
    relative_path: &'static str,
}

const TRANSPORT_SECRETS: &[SecretLocation] = &[SecretLocation {
    source_variable: "COMMITARIUM_SIMULATED_WORKER_TOKEN_SOURCE",
    relative_path: ".commitarium/internal/simulated-worker-token",
}];

/// Bearer tokens for agent workers live in one directory that the coordinator
/// mounts, so a new agent's token is readable without a restart (ADR-016).
const AGENT_WORKER_TOKEN_DIR: SecretLocation = SecretLocation {
    source_variable: "COMMITARIUM_AGENT_WORKER_TOKEN_DIR_SOURCE",
    relative_path: ".commitarium/internal/agent-workers",
};

const ADMIN_TOKEN: SecretLocation = SecretLocation {
    source_variable: "COMMITARIUM_FORGEJO_TOKEN_SOURCE",
    relative_path: ".commitarium/forgejo-token",
};
const ADMIN_USERNAME: &str = "commitarium_admin";
const ADMIN_SCOPES: &str = "write:admin,write:user,write:repository,write:issue";
const AGENT_SCOPES: &str = "write:repository,write:issue";

/// One Forgejo user Commitarium owns and the private file holding its token.
struct ForgejoIdentity {
    username: String,
    email: String,
    admin: bool,
    token_path: PathBuf,
    scopes: &'static str,
}

fn admin_identity(root: &Path) -> ForgejoIdentity {
    ForgejoIdentity {
        username: ADMIN_USERNAME.to_string(),
        email: "admin@commitarium.local".to_string(),
        admin: true,
        token_path: secret_path(root, &ADMIN_TOKEN),
        scopes: ADMIN_SCOPES,
    }
}

/// Each agent has a lead and a reviewer identity (ADR-016): Forgejo refuses an
/// approval from the pull request's author, and the pair keeps the audit trail
/// showing which account wrote and which approved. The agents migrated from
/// the provider profiles keep those profiles' users and token files.
fn agent_identities(root: &Path, agents: &[agents::Agent]) -> Vec<ForgejoIdentity> {
    agents
        .iter()
        .flat_map(|agent| {
            ["lead", "reviewer"].map(|role| {
                let username = format!("{}-{role}", agent.id);
                ForgejoIdentity {
                    email: format!("{username}@commitarium.local"),
                    admin: false,
                    token_path: agent_forgejo_token_path(root, &agent.id, role),
                    scopes: AGENT_SCOPES,
                    username,
                }
            })
        })
        .collect()
}

/// The Forgejo token file for an agent's role, relative to the Compose root
/// as the generated worker service mounts it.
pub(crate) fn agent_forgejo_token_relative_path(agent_id: &str, role: &str) -> String {
    format!(".commitarium/agents/{agent_id}-{role}/forgejo-token")
}

fn agent_forgejo_token_path(root: &Path, agent_id: &str, role: &str) -> PathBuf {
    root.join(agent_forgejo_token_relative_path(agent_id, role))
}

/// The directory holding agent worker bearer tokens.
pub(crate) fn agent_worker_token_dir(compose_file: &Path) -> Result<PathBuf, String> {
    Ok(secret_path(
        compose_root(compose_file)?,
        &AGENT_WORKER_TOKEN_DIR,
    ))
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ForgejoViewerStatus {
    configured: bool,
    username: &'static str,
    login_url: String,
}

#[derive(Clone, Debug, Deserialize)]
struct ForgejoUser {
    login: String,
    active: bool,
    is_admin: bool,
    prohibit_login: bool,
    restricted: bool,
}

impl ForgejoUser {
    fn is_configured_viewer(&self) -> bool {
        self.login == FORGEJO_VIEWER_USERNAME
            && self.active
            && !self.is_admin
            && !self.prohibit_login
            && self.restricted
    }
}

#[derive(Deserialize)]
struct ForgejoRepositoryOwner {
    login: String,
}

#[derive(Deserialize)]
struct ForgejoRepository {
    name: String,
    owner: ForgejoRepositoryOwner,
}

struct ForgejoApi {
    base_url: Url,
    client: reqwest::Client,
}

fn compose_root(compose_file: &Path) -> Result<&Path, String> {
    compose_file
        .parent()
        .ok_or_else(|| "Compose file has no parent directory".to_string())
}

fn secret_path(root: &Path, location: &SecretLocation) -> PathBuf {
    std::env::var(location.source_variable)
        .ok()
        .filter(|value| !value.trim().is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| root.join(location.relative_path))
}

fn forgejo_host_base_url() -> Result<Url, String> {
    let configured = if let Ok(value) = std::env::var("COMMITARIUM_FORGEJO_HOST_URL") {
        value
    } else {
        let port = match std::env::var("COMMITARIUM_FORGEJO_PORT") {
            Ok(value) => value
                .parse::<u16>()
                .ok()
                .filter(|port| *port > 0)
                .ok_or_else(|| "COMMITARIUM_FORGEJO_PORT must be a valid TCP port".to_string())?,
            Err(_) => FORGEJO_DEFAULT_PORT,
        };
        format!("http://127.0.0.1:{port}/")
    };
    let mut parsed = Url::parse(configured.trim())
        .map_err(|_| "the Forgejo browser URL is invalid".to_string())?;
    let loopback = parsed
        .host_str()
        .is_some_and(|host| matches!(host, "127.0.0.1" | "localhost" | "::1"));
    if parsed.scheme() != "http"
        || !loopback
        || parsed.username() != ""
        || parsed.password().is_some()
        || parsed.query().is_some()
        || parsed.fragment().is_some()
    {
        return Err("the Forgejo browser URL must be a credential-free loopback HTTP URL".into());
    }
    parsed.set_path("/");
    Ok(parsed)
}

fn validate_viewer_password(password: &str) -> Result<(), String> {
    if password.chars().count() < 6 {
        return Err("the audit viewer password must be at least 6 characters".to_string());
    }
    if password.len() > 255 {
        return Err("the audit viewer password must be at most 255 bytes".to_string());
    }
    if password.chars().any(char::is_control) {
        return Err("the audit viewer password cannot contain control characters".to_string());
    }
    Ok(())
}

impl ForgejoApi {
    async fn new(compose_file: &Path) -> Result<Self, String> {
        let root = compose_root(compose_file)?;
        let token_path = secret_path(root, &ADMIN_TOKEN);
        let token = tokio::fs::read(&token_path)
            .await
            .map(Zeroizing::new)
            .map_err(|_| {
                "the internal Forgejo administrator credential is unavailable".to_string()
            })?;
        let token = trim_ascii_whitespace(&token);
        validate_secret_bytes(token)?;

        let mut encoded = Zeroizing::new(Vec::with_capacity(6 + token.len()));
        encoded.extend_from_slice(b"token ");
        encoded.extend_from_slice(token);
        let mut authorization = HeaderValue::from_bytes(&encoded)
            .map_err(|_| "the internal Forgejo administrator credential is invalid".to_string())?;
        authorization.set_sensitive(true);
        let mut headers = HeaderMap::new();
        headers.insert(AUTHORIZATION, authorization);

        let client = reqwest::Client::builder()
            .default_headers(headers)
            .redirect(reqwest::redirect::Policy::none())
            .timeout(FORGEJO_REQUEST_TIMEOUT)
            .build()
            .map_err(|_| "the Forgejo audit-viewer client could not start".to_string())?;
        Ok(Self {
            base_url: forgejo_host_base_url()?,
            client,
        })
    }

    fn endpoint(&self, segments: &[&str]) -> Result<Url, String> {
        let mut url = self.base_url.clone();
        let mut path = url
            .path_segments_mut()
            .map_err(|_| "the Forgejo browser URL cannot be used for API requests".to_string())?;
        path.pop_if_empty();
        path.extend(["api", "v1"]);
        path.extend(segments.iter().copied());
        drop(path);
        Ok(url)
    }

    async fn viewer(&self) -> Result<Option<ForgejoUser>, String> {
        let response = self
            .client
            .get(self.endpoint(&["users", FORGEJO_VIEWER_USERNAME])?)
            .send()
            .await
            .map_err(|_| "Forgejo is unavailable while checking audit access".to_string())?;
        match response.status() {
            StatusCode::OK => response
                .json()
                .await
                .map(Some)
                .map_err(|_| "Forgejo returned an invalid audit-viewer account".to_string()),
            StatusCode::NOT_FOUND => Ok(None),
            status => Err(format!(
                "Forgejo could not check the audit-viewer account (HTTP {status})"
            )),
        }
    }

    async fn configure_viewer(&self, password: &str) -> Result<(), String> {
        #[derive(Serialize)]
        struct CreateViewer<'a> {
            username: &'static str,
            email: &'static str,
            full_name: &'static str,
            password: &'a str,
            must_change_password: bool,
            restricted: bool,
            send_notify: bool,
            visibility: &'static str,
        }
        #[derive(Serialize)]
        struct EditViewer<'a> {
            email: &'static str,
            full_name: &'static str,
            password: &'a str,
            must_change_password: bool,
            active: bool,
            admin: bool,
            allow_git_hook: bool,
            allow_import_local: bool,
            max_repo_creation: i32,
            prohibit_login: bool,
            allow_create_organization: bool,
            restricted: bool,
            visibility: &'static str,
            hide_email: bool,
        }

        if self.viewer().await?.is_none() {
            let response = self
                .client
                .post(self.endpoint(&["admin", "users"])?)
                .json(&CreateViewer {
                    username: FORGEJO_VIEWER_USERNAME,
                    email: FORGEJO_VIEWER_EMAIL,
                    full_name: FORGEJO_VIEWER_FULL_NAME,
                    password,
                    must_change_password: false,
                    restricted: true,
                    send_notify: false,
                    visibility: "private",
                })
                .send()
                .await
                .map_err(|_| "Forgejo is unavailable while creating audit access".to_string())?;
            if !matches!(
                response.status(),
                StatusCode::CREATED | StatusCode::CONFLICT
            ) {
                return Err(format!(
                    "Forgejo rejected the audit-viewer account (HTTP {})",
                    response.status()
                ));
            }
        }

        let response = self
            .client
            .patch(self.endpoint(&["admin", "users", FORGEJO_VIEWER_USERNAME])?)
            .json(&EditViewer {
                email: FORGEJO_VIEWER_EMAIL,
                full_name: FORGEJO_VIEWER_FULL_NAME,
                password,
                must_change_password: false,
                active: true,
                admin: false,
                allow_git_hook: false,
                allow_import_local: false,
                max_repo_creation: 0,
                prohibit_login: false,
                allow_create_organization: false,
                restricted: true,
                visibility: "private",
                hide_email: true,
            })
            .send()
            .await
            .map_err(|_| "Forgejo is unavailable while securing audit access".to_string())?;
        if response.status() != StatusCode::OK {
            return Err(format!(
                "Forgejo rejected the audit-viewer settings (HTTP {})",
                response.status()
            ));
        }
        Ok(())
    }

    async fn owned_repositories(&self) -> Result<Vec<ForgejoRepository>, String> {
        let owner = ADMIN_USERNAME;
        let mut repositories = Vec::new();
        for page in 1..=FORGEJO_MAX_REPOSITORY_PAGES {
            let mut url = self.endpoint(&["user", "repos"])?;
            url.query_pairs_mut()
                .append_pair("page", &page.to_string())
                .append_pair("limit", &FORGEJO_REPOSITORY_PAGE_SIZE.to_string());
            let response = self.client.get(url).send().await.map_err(|_| {
                "Forgejo is unavailable while listing internal repositories".to_string()
            })?;
            if response.status() != StatusCode::OK {
                return Err(format!(
                    "Forgejo could not list internal repositories (HTTP {})",
                    response.status()
                ));
            }
            let page_repositories: Vec<ForgejoRepository> = response
                .json()
                .await
                .map_err(|_| "Forgejo returned an invalid repository list".to_string())?;
            let count = page_repositories.len();
            repositories.extend(
                page_repositories
                    .into_iter()
                    .filter(|repository| repository.owner.login == owner),
            );
            if count < FORGEJO_REPOSITORY_PAGE_SIZE {
                return Ok(repositories);
            }
        }
        Err("Forgejo returned too many repository pages to reconcile safely".to_string())
    }

    async fn grant_viewer_access(&self, repository: &ForgejoRepository) -> Result<(), String> {
        #[derive(Serialize)]
        struct Permission<'a> {
            permission: &'a str,
        }
        let response = self
            .client
            .request(
                Method::PUT,
                self.endpoint(&[
                    "repos",
                    &repository.owner.login,
                    &repository.name,
                    "collaborators",
                    FORGEJO_VIEWER_USERNAME,
                ])?,
            )
            .json(&Permission { permission: "read" })
            .send()
            .await
            .map_err(|_| "Forgejo is unavailable while granting audit access".to_string())?;
        if !matches!(
            response.status(),
            StatusCode::OK | StatusCode::CREATED | StatusCode::NO_CONTENT
        ) {
            return Err(format!(
                "Forgejo could not grant audit access to an internal repository (HTTP {})",
                response.status()
            ));
        }
        Ok(())
    }

    async fn backfill_viewer_access(&self) -> Result<(), String> {
        for repository in self.owned_repositories().await? {
            self.grant_viewer_access(&repository).await?;
        }
        Ok(())
    }

    fn status(&self, viewer: Option<&ForgejoUser>) -> ForgejoViewerStatus {
        let mut login_url = self.base_url.clone();
        login_url.set_path("/user/login");
        ForgejoViewerStatus {
            configured: viewer.is_some_and(ForgejoUser::is_configured_viewer),
            username: FORGEJO_VIEWER_USERNAME,
            login_url: login_url.to_string(),
        }
    }
}

#[tauri::command]
pub async fn get_forgejo_viewer_status() -> Result<ForgejoViewerStatus, String> {
    let compose_file = docker::compose_file()?;
    let api = ForgejoApi::new(&compose_file).await?;
    let viewer = api.viewer().await?;
    Ok(api.status(viewer.as_ref()))
}

#[tauri::command]
pub async fn configure_forgejo_viewer(password: String) -> Result<ForgejoViewerStatus, String> {
    let password = Zeroizing::new(password);
    validate_viewer_password(&password)?;
    let compose_file = docker::compose_file()?;
    let api = ForgejoApi::new(&compose_file).await?;
    api.configure_viewer(&password).await?;
    api.backfill_viewer_access().await?;
    let viewer = api.viewer().await?;
    if !viewer
        .as_ref()
        .is_some_and(ForgejoUser::is_configured_viewer)
    {
        return Err("Forgejo did not preserve the secured audit-viewer account".to_string());
    }
    Ok(api.status(viewer.as_ref()))
}

/// Ensure all coordinator-to-worker bearer tokens exist before Compose can
/// turn a missing bind-mount source into a directory. The result says whether
/// any source changed, so the launcher can recreate containers whose old mount
/// was created while that source had the wrong type.
pub(crate) fn prepare_transport_secrets(compose_file: &Path) -> Result<bool, String> {
    let root = compose_root(compose_file)?;
    let mut changed = false;
    for location in TRANSPORT_SECRETS {
        changed |= ensure_random_secret(&secret_path(root, location))?;
    }
    let directory = secret_path(root, &AGENT_WORKER_TOKEN_DIR);
    fs::create_dir_all(&directory)
        .map_err(|_| format!("could not create private directory {}", directory.display()))?;
    set_private_directory_permissions(&directory)?;
    Ok(changed)
}

/// Ensure each agent worker has a bearer token, named as the coordinator
/// expects (`agent-<id>-worker-token`). Returns whether any token was created.
pub(crate) fn prepare_agent_worker_tokens(
    compose_file: &Path,
    agents: &[agents::Agent],
) -> Result<bool, String> {
    let directory = agent_worker_token_dir(compose_file)?;
    let mut changed = false;
    for agent in agents {
        changed |= ensure_random_secret(&directory.join(agents::worker_token_file(&agent.id)))?;
    }
    Ok(changed)
}

/// Create a random private token at path unless a valid one is already there.
fn ensure_random_secret(path: &Path) -> Result<bool, String> {
    if secret_is_ready(path)? {
        return Ok(false);
    }
    let mut bytes = Zeroizing::new(vec![0_u8; 32]);
    random_fill(bytes.as_mut_slice())
        .map_err(|_| "secure randomness is unavailable".to_string())?;
    let mut token = Zeroizing::new(String::with_capacity(bytes.len() * 2));
    for byte in bytes.iter() {
        use std::fmt::Write as _;
        write!(&mut *token, "{byte:02x}")
            .map_err(|_| "could not encode an internal credential".to_string())?;
    }
    write_secret(path, token.as_bytes())?;
    Ok(true)
}

/// Ensure Forgejo credential bind sources are regular private files before a
/// restore creates all service containers. The files intentionally remain
/// empty: after Forgejo data is restored, ordinary bootstrap replaces them
/// with newly generated tokens for the restored identities.
pub(crate) fn prepare_forgejo_secret_mounts(
    compose_file: &Path,
    agents: &[agents::Agent],
) -> Result<(), String> {
    let root = compose_root(compose_file)?;
    let mut identities = vec![admin_identity(root)];
    identities.extend(agent_identities(root, agents));
    for identity in &identities {
        let path = &identity.token_path;
        if secret_is_ready(path)? {
            continue;
        }
        let parent = path
            .parent()
            .ok_or_else(|| format!("private file {} has no parent directory", path.display()))?;
        fs::create_dir_all(parent)
            .map_err(|_| format!("could not create private directory {}", parent.display()))?;
        set_private_directory_permissions(parent)?;
        OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .open(path)
            .map_err(|_| format!("could not create private file {}", path.display()))?;
        set_private_file_permissions(path)?;
    }
    Ok(())
}

/// Create any missing Forgejo identities and scoped credentials after Forgejo
/// itself is running. Credential values are written directly to private files
/// and are never returned across Tauri IPC.
pub(crate) fn provision_forgejo(compose_file: &Path, project_name: &str) -> Result<bool, String> {
    let root = compose_root(compose_file)?;
    let mut admin = DockerForgejoAdmin::new(compose_file, project_name);
    let users = wait_for_users(&mut admin)?;
    ensure_forgejo_credentials(&mut admin, users, &[admin_identity(root)])
}

/// Create any missing lead and reviewer identities for the agents, with their
/// scoped tokens. Forgejo is asked only when some token is missing, so this
/// is cheap for agents that are already set up; it must then be running
/// (`wait` allows for it still starting).
pub(crate) fn provision_agent_identities(
    compose_file: &Path,
    project_name: &str,
    agents: &[agents::Agent],
    wait: bool,
) -> Result<bool, String> {
    let root = compose_root(compose_file)?;
    let identities = agent_identities(root, agents);
    let mut ready = true;
    for identity in &identities {
        ready &= token_scopes_are_ready(&identity.token_path, identity.scopes)?;
    }
    if ready {
        return Ok(false);
    }
    let mut admin = DockerForgejoAdmin::new(compose_file, project_name);
    let users = if wait {
        wait_for_users(&mut admin)?
    } else {
        admin.list_users()?
    };
    ensure_forgejo_credentials(&mut admin, users, &identities)
}

trait ForgejoAdmin {
    fn list_users(&mut self) -> Result<HashSet<String>, String>;
    fn create_user(&mut self, identity: &ForgejoIdentity) -> Result<(), String>;
    fn generate_token(
        &mut self,
        username: &str,
        scopes: &str,
    ) -> Result<Zeroizing<Vec<u8>>, String>;
}

fn wait_for_users(admin: &mut impl ForgejoAdmin) -> Result<HashSet<String>, String> {
    let mut last_error = "Forgejo is not ready".to_string();
    for _ in 0..FORGEJO_READY_ATTEMPTS {
        match admin.list_users() {
            Ok(users) => return Ok(users),
            Err(error) => last_error = error,
        }
        thread::sleep(FORGEJO_READY_DELAY);
    }
    Err(last_error)
}

fn ensure_forgejo_credentials(
    admin: &mut impl ForgejoAdmin,
    mut users: HashSet<String>,
    identities: &[ForgejoIdentity],
) -> Result<bool, String> {
    let mut changed = false;
    for identity in identities {
        let created = if users.contains(&identity.username) {
            false
        } else {
            admin.create_user(identity)?;
            users.insert(identity.username.clone());
            true
        };
        let path = &identity.token_path;
        if !created && token_scopes_are_ready(path, identity.scopes)? {
            continue;
        }
        let token = admin.generate_token(&identity.username, identity.scopes)?;
        validate_secret_bytes(&token)?;
        write_secret(path, &token)?;
        write_secret(&token_scope_marker_path(path), identity.scopes.as_bytes())?;
        changed = true;
    }
    Ok(changed)
}

fn token_scope_marker_path(token_path: &Path) -> PathBuf {
    let mut marker = token_path.as_os_str().to_os_string();
    marker.push(".scopes");
    PathBuf::from(marker)
}

fn token_scopes_are_ready(token_path: &Path, scopes: &str) -> Result<bool, String> {
    if !secret_is_ready(token_path)? {
        return Ok(false);
    }
    let marker = token_scope_marker_path(token_path);
    if !secret_is_ready(&marker)? {
        return Ok(false);
    }
    let stored = fs::read(&marker)
        .map(Zeroizing::new)
        .map_err(|_| format!("could not read private file {}", marker.display()))?;
    Ok(trim_ascii_whitespace(&stored) == scopes.as_bytes())
}

struct DockerForgejoAdmin {
    compose_file: PathBuf,
    project_name: String,
}

impl DockerForgejoAdmin {
    fn new(compose_file: &Path, project_name: &str) -> Self {
        Self {
            compose_file: compose_file.to_path_buf(),
            project_name: project_name.to_string(),
        }
    }

    fn command(&self) -> Command {
        let mut command = docker::docker_command();
        command.args(["compose", "-f"]);
        command.arg(&self.compose_file);
        command.args(["-p", &self.project_name, "exec", "-T", "forgejo", "forgejo"]);
        command
    }
}

impl ForgejoAdmin for DockerForgejoAdmin {
    fn list_users(&mut self) -> Result<HashSet<String>, String> {
        let output = self
            .command()
            .args(["admin", "user", "list"])
            .output()
            .map_err(|_| "could not inspect Forgejo users".to_string())?;
        if !output.status.success() {
            return Err("Forgejo is not ready for identity setup".to_string());
        }
        let stdout = String::from_utf8(output.stdout)
            .map_err(|_| "Forgejo returned invalid user-list output".to_string())?;
        Ok(parse_usernames(&stdout))
    }

    fn create_user(&mut self, identity: &ForgejoIdentity) -> Result<(), String> {
        let mut command = self.command();
        command.args([
            "admin",
            "user",
            "create",
            "--username",
            &identity.username,
            "--email",
            &identity.email,
            "--random-password",
            "--must-change-password=false",
        ]);
        if identity.admin {
            command.arg("--admin");
        } else {
            command.arg("--restricted");
        }
        let status = command
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()
            .map_err(|_| format!("could not create Forgejo identity {}", identity.username))?;
        if !status.success() {
            return Err(format!(
                "could not create Forgejo identity {}",
                identity.username
            ));
        }
        Ok(())
    }

    fn generate_token(
        &mut self,
        username: &str,
        scopes: &str,
    ) -> Result<Zeroizing<Vec<u8>>, String> {
        let token_name = format!("commitarium-bootstrap-{}", random_hex(8)?);
        let output = self
            .command()
            .args([
                "admin",
                "user",
                "generate-access-token",
                "--username",
                username,
                "--token-name",
                &token_name,
                "--scopes",
                scopes,
                "--raw",
            ])
            .output()
            .map_err(|_| format!("could not create Forgejo token for {username}"))?;
        let status = output.status;
        let stdout = Zeroizing::new(output.stdout);
        let _stderr = Zeroizing::new(output.stderr);
        if !status.success() {
            return Err(format!("could not create Forgejo token for {username}"));
        }
        Ok(stdout)
    }
}

fn parse_usernames(output: &str) -> HashSet<String> {
    output
        .lines()
        .filter_map(|line| {
            let columns: Vec<_> = line.split_whitespace().collect();
            if columns.len() >= 2 && columns[0].parse::<u64>().is_ok() {
                Some(columns[1].to_string())
            } else {
                None
            }
        })
        .collect()
}

fn random_hex(byte_count: usize) -> Result<String, String> {
    let mut bytes = Zeroizing::new(vec![0_u8; byte_count]);
    random_fill(bytes.as_mut_slice())
        .map_err(|_| "secure randomness is unavailable".to_string())?;
    let mut result = String::with_capacity(byte_count * 2);
    for byte in bytes.iter() {
        use std::fmt::Write as _;
        write!(&mut result, "{byte:02x}")
            .map_err(|_| "could not encode a credential identifier".to_string())?;
    }
    Ok(result)
}

fn secret_is_ready(path: &Path) -> Result<bool, String> {
    let metadata = match fs::symlink_metadata(path) {
        Ok(metadata) => metadata,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(false),
        Err(_) => return Err(format!("could not inspect private file {}", path.display())),
    };
    if metadata.file_type().is_symlink() {
        return Err(format!(
            "private file {} must not be a symlink",
            path.display()
        ));
    }
    if metadata.is_dir() {
        fs::remove_dir(path).map_err(|_| {
            format!(
                "private file {} is a non-empty directory; move its contents and retry",
                path.display()
            )
        })?;
        return Ok(false);
    }
    if !metadata.is_file() {
        return Err(format!("private path {} is not a file", path.display()));
    }
    let bytes = fs::read(path)
        .map(Zeroizing::new)
        .map_err(|_| format!("could not read private file {}", path.display()))?;
    if bytes.iter().all(u8::is_ascii_whitespace) {
        return Ok(false);
    }
    validate_secret_bytes(&bytes)?;
    set_private_file_permissions(path)?;
    Ok(true)
}

fn validate_secret_bytes(bytes: &[u8]) -> Result<(), String> {
    let trimmed = trim_ascii_whitespace(bytes);
    if trimmed.is_empty() || trimmed.iter().any(u8::is_ascii_whitespace) {
        return Err("a generated private credential had an invalid format".to_string());
    }
    Ok(())
}

fn trim_ascii_whitespace(mut bytes: &[u8]) -> &[u8] {
    while bytes.first().is_some_and(u8::is_ascii_whitespace) {
        bytes = &bytes[1..];
    }
    while bytes.last().is_some_and(u8::is_ascii_whitespace) {
        bytes = &bytes[..bytes.len() - 1];
    }
    bytes
}

fn write_secret(path: &Path, secret: &[u8]) -> Result<(), String> {
    validate_secret_bytes(secret)?;
    let parent = path
        .parent()
        .ok_or_else(|| format!("private file {} has no parent directory", path.display()))?;
    fs::create_dir_all(parent)
        .map_err(|_| format!("could not create private directory {}", parent.display()))?;
    set_private_directory_permissions(parent)?;

    let temporary = parent.join(format!(".commitarium-secret-{}", random_hex(8)?));
    let result = (|| {
        let mut file = OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&temporary)
            .map_err(|_| "could not create temporary private file".to_string())?;
        set_private_file_permissions(&temporary)?;
        file.write_all(trim_ascii_whitespace(secret))
            .map_err(|_| "could not write private credential".to_string())?;
        file.sync_all()
            .map_err(|_| "could not persist private credential".to_string())?;
        drop(file);
        match fs::symlink_metadata(path) {
            Ok(metadata) if metadata.is_dir() => fs::remove_dir(path)
                .map_err(|_| format!("private file {} is a non-empty directory", path.display()))?,
            Ok(metadata) if metadata.is_file() => fs::remove_file(path)
                .map_err(|_| format!("could not replace private file {}", path.display()))?,
            Ok(_) => {
                return Err(format!(
                    "private path {} cannot be replaced",
                    path.display()
                ))
            }
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(_) => return Err(format!("could not inspect private file {}", path.display())),
        }
        fs::rename(&temporary, path)
            .map_err(|_| format!("could not install private file {}", path.display()))?;
        set_private_file_permissions(path)
    })();
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result
}

#[cfg(unix)]
fn set_private_directory_permissions(path: &Path) -> Result<(), String> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(0o700))
        .map_err(|_| format!("could not protect private directory {}", path.display()))
}

#[cfg(not(unix))]
fn set_private_directory_permissions(_path: &Path) -> Result<(), String> {
    Ok(())
}

#[cfg(unix)]
fn set_private_file_permissions(path: &Path) -> Result<(), String> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(0o600))
        .map_err(|_| format!("could not protect private file {}", path.display()))
}

#[cfg(not(unix))]
fn set_private_file_permissions(_path: &Path) -> Result<(), String> {
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    #[derive(Default)]
    struct FakeAdmin {
        users: HashSet<String>,
        created: Vec<String>,
        generated: Vec<(String, String)>,
        tokens: HashMap<String, Vec<u8>>,
    }

    impl ForgejoAdmin for FakeAdmin {
        fn list_users(&mut self) -> Result<HashSet<String>, String> {
            Ok(self.users.clone())
        }

        fn create_user(&mut self, identity: &ForgejoIdentity) -> Result<(), String> {
            self.created.push(identity.username.to_string());
            self.users.insert(identity.username.to_string());
            Ok(())
        }

        fn generate_token(
            &mut self,
            username: &str,
            scopes: &str,
        ) -> Result<Zeroizing<Vec<u8>>, String> {
            self.generated
                .push((username.to_string(), scopes.to_string()));
            Ok(Zeroizing::new(
                self.tokens
                    .get(username)
                    .cloned()
                    .unwrap_or_else(|| format!("token-{username}").into_bytes()),
            ))
        }
    }

    fn write_current_forgejo_token(identity: &ForgejoIdentity, token: &[u8]) {
        let path = &identity.token_path;
        fs::create_dir_all(path.parent().expect("token parent")).expect("create token parent");
        write_secret(path, token).expect("write token");
        write_secret(&token_scope_marker_path(path), identity.scopes.as_bytes())
            .expect("write token scopes");
    }

    fn test_agents() -> Vec<agents::Agent> {
        vec![
            agents::Agent::new("codex", "Codex", agents::Provider::Codex),
            agents::Agent::new("claude-max", "Claude Max", agents::Provider::Claude),
        ]
    }

    /// The administrator followed by each agent's lead and reviewer.
    fn test_identities(root: &Path) -> Vec<ForgejoIdentity> {
        let mut identities = vec![admin_identity(root)];
        identities.extend(agent_identities(root, &test_agents()));
        identities
    }

    #[test]
    fn agents_get_a_lead_and_reviewer_identity_at_stable_paths() {
        let root = Path::new("/runtime");
        let identities = agent_identities(root, &test_agents());
        let names: Vec<_> = identities
            .iter()
            .map(|identity| identity.username.as_str())
            .collect();
        assert_eq!(
            names,
            [
                "codex-lead",
                "codex-reviewer",
                "claude-max-lead",
                "claude-max-reviewer"
            ]
        );
        // The migrated agent keeps the provider profile's token file.
        assert_eq!(
            identities[0].token_path,
            root.join(".commitarium/agents/codex-lead/forgejo-token")
        );
        assert!(identities
            .iter()
            .all(|identity| !identity.admin && identity.scopes == AGENT_SCOPES));
    }

    #[test]
    fn agent_worker_tokens_are_created_once_per_agent() {
        let root = tempfile::tempdir().expect("temp root");
        let compose_file = root.path().join("compose.yml");
        fs::write(&compose_file, "services: {}").expect("compose file");
        prepare_transport_secrets(&compose_file).expect("transport secrets");
        assert!(prepare_agent_worker_tokens(&compose_file, &test_agents()).expect("first"));
        assert!(!prepare_agent_worker_tokens(&compose_file, &test_agents()).expect("second"));
        let directory = agent_worker_token_dir(&compose_file).expect("token directory");
        assert!(directory.join("agent-claude-max-worker-token").is_file());
        assert!(directory.join("agent-codex-worker-token").is_file());
    }

    #[test]
    fn transport_bootstrap_creates_and_preserves_private_tokens() {
        let root = tempfile::tempdir().expect("temp root");
        let compose_file = root.path().join("compose.yml");
        fs::write(&compose_file, "services: {}").expect("compose file");

        assert!(prepare_transport_secrets(&compose_file).expect("first bootstrap"));
        let first: Vec<Vec<u8>> = TRANSPORT_SECRETS
            .iter()
            .map(|location| fs::read(secret_path(root.path(), location)).expect("token"))
            .collect();
        assert!(!prepare_transport_secrets(&compose_file).expect("second bootstrap"));

        for (index, location) in TRANSPORT_SECRETS.iter().enumerate() {
            let path = secret_path(root.path(), location);
            let second = fs::read(&path).expect("preserved token");
            assert_eq!(first[index], second);
            assert_eq!(second.len(), 64);
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                assert_eq!(
                    fs::metadata(path).expect("metadata").permissions().mode() & 0o777,
                    0o600
                );
            }
        }
    }

    #[test]
    fn restore_mount_bootstrap_creates_empty_files_without_replacing_tokens() {
        let root = tempfile::tempdir().expect("temp root");
        let compose_file = root.path().join("compose.yml");
        fs::write(&compose_file, "services: {}").expect("compose file");
        let identities = test_identities(root.path());
        let preserved = &identities[0].token_path;
        fs::create_dir_all(preserved.parent().expect("parent")).expect("parent");
        fs::write(preserved, b"existing-token").expect("existing token");

        prepare_forgejo_secret_mounts(&compose_file, &test_agents())
            .expect("prepare restore mounts");

        assert_eq!(fs::read(preserved).expect("token"), b"existing-token");
        for identity in &identities[1..] {
            let path = &identity.token_path;
            assert!(path.is_file());
            assert!(fs::read(path).expect("placeholder").is_empty());
        }
    }

    #[test]
    fn forgejo_bootstrap_adopts_users_repairs_empty_mount_directories_and_is_idempotent() {
        let root = tempfile::tempdir().expect("temp root");
        let identities = test_identities(root.path());
        let existing = &identities[0];
        write_current_forgejo_token(existing, b"existing-token");

        let broken = &identities[1];
        fs::create_dir_all(&broken.token_path).expect("empty mount directory");

        let mut admin = FakeAdmin::default();
        admin.users.insert(existing.username.clone());
        let users = admin.users.clone();
        assert!(
            ensure_forgejo_credentials(&mut admin, users, &identities).expect("first bootstrap")
        );

        assert_eq!(
            fs::read(&existing.token_path).expect("existing token"),
            b"existing-token"
        );
        assert_eq!(admin.created.len(), identities.len() - 1);
        assert_eq!(admin.generated.len(), identities.len() - 1);
        assert!(admin
            .generated
            .iter()
            .all(|(_, scopes)| !scopes.contains("all")));
        assert!(identities[0]
            .scopes
            .split(',')
            .any(|scope| scope == "write:admin"));
        assert!(broken.token_path.is_file());

        let created = admin.created.len();
        let generated = admin.generated.len();
        let users = admin.users.clone();
        assert!(
            !ensure_forgejo_credentials(&mut admin, users, &identities).expect("second bootstrap")
        );
        assert_eq!(admin.created.len(), created);
        assert_eq!(admin.generated.len(), generated);
    }

    #[test]
    fn forgejo_bootstrap_rotates_a_token_when_its_scope_marker_is_missing() {
        let root = tempfile::tempdir().expect("temp root");
        let identities = test_identities(root.path());
        let mut admin = FakeAdmin::default();
        for identity in &identities {
            admin.users.insert(identity.username.clone());
            write_current_forgejo_token(identity, b"current-token");
        }

        let administrator = &identities[0];
        let administrator_path = administrator.token_path.clone();
        fs::remove_file(token_scope_marker_path(&administrator_path)).expect("remove old marker");
        admin.tokens.insert(
            administrator.username.clone(),
            b"upgraded-admin-token".to_vec(),
        );

        let users = admin.users.clone();
        assert!(
            ensure_forgejo_credentials(&mut admin, users, &identities).expect("upgrade old token")
        );
        assert_eq!(
            admin.generated,
            vec![(
                administrator.username.to_string(),
                administrator.scopes.to_string()
            )]
        );
        assert_eq!(
            fs::read(administrator_path).expect("upgraded token"),
            b"upgraded-admin-token"
        );
    }

    #[test]
    fn username_parser_ignores_headers_and_noise() {
        let parsed = parse_usernames(
            "ID Username Email IsActive\n1 commitarium_admin admin@example.test true\nwarning\n2 claude-lead claude@example.test true\n",
        );
        assert_eq!(
            parsed,
            HashSet::from(["commitarium_admin".to_string(), "claude-lead".to_string()])
        );
    }

    #[test]
    fn audit_viewer_password_has_a_bounded_non_control_contract() {
        assert!(validate_viewer_password("sixsix").is_ok());
        assert!(validate_viewer_password("short").is_err());
        assert!(validate_viewer_password("sixsix\n").is_err());
        assert!(validate_viewer_password(&"x".repeat(256)).is_err());
    }

    #[test]
    fn only_the_restricted_non_admin_account_is_a_configured_viewer() {
        let expected = ForgejoUser {
            login: FORGEJO_VIEWER_USERNAME.to_string(),
            active: true,
            is_admin: false,
            prohibit_login: false,
            restricted: true,
        };
        assert!(expected.is_configured_viewer());

        let cases = [
            ForgejoUser {
                is_admin: true,
                ..expected.clone()
            },
            ForgejoUser {
                restricted: false,
                ..expected.clone()
            },
            ForgejoUser {
                active: false,
                ..expected.clone()
            },
        ];
        assert!(cases.iter().all(|viewer| !viewer.is_configured_viewer()));
    }

    #[test]
    fn audit_viewer_status_serializes_for_the_frontend_contract() {
        let value = serde_json::to_value(ForgejoViewerStatus {
            configured: true,
            username: FORGEJO_VIEWER_USERNAME,
            login_url: "http://127.0.0.1:3001/user/login".to_string(),
        })
        .expect("serialize viewer status");
        assert_eq!(value["configured"], true);
        assert_eq!(value["username"], FORGEJO_VIEWER_USERNAME);
        assert_eq!(value["loginUrl"], "http://127.0.0.1:3001/user/login");
        assert!(value.get("login_url").is_none());
    }

    #[test]
    fn non_empty_directory_is_never_destroyed_as_a_secret_file_repair() {
        let root = tempfile::tempdir().expect("temp root");
        let path = root.path().join("token");
        fs::create_dir(&path).expect("directory");
        fs::write(path.join("keep"), b"user data").expect("user data");
        let error = secret_is_ready(&path).expect_err("non-empty directory must fail");
        assert!(error.contains("non-empty directory"));
        assert!(path.join("keep").exists());
    }
}
