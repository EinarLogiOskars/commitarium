//! authentik on the server (COMPANY_SIGN_IN_PLAN.md, C4): for a company
//! without a sign-in provider that would rather not run one itself.
//! Commitarium runs it (server, worker and its database, in one Compose
//! project), makes its admin and the application the gate signs in with,
//! and connects it. It costs 2 to 3 GB of memory, which the app says before
//! anyone installs it.
//!
//! Its admin password and the secrets live in `authentik.json`, beside the
//! other state a backup carries; its database is backed up with deployed
//! apps' data.
//!
//! One authentik serves every domain that uses it (MULTI_DOMAIN_PLAN.md,
//! M4), at `auth.<the domain it was set up on>`. Each domain gets its own
//! provider and application, with its own client and redirect address, and
//! once there are two, each application is bound to its domain's group, so
//! "anyone at the company" means that domain's people.

use crate::docker;
use crate::runtime::block_on;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::collections::BTreeMap;
use std::fs;
use std::io::ErrorKind;
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

/// Its address: `auth.<domain>`.
pub const NAME: &str = "auth";
pub const FILE: &str = "authentik.json";
const DIR: &str = "authentik";
pub const PROJECT: &str = "commitarium-authentik";
const IMAGE: &str = "ghcr.io/goauthentik/server:2026.2.7";
const DATABASE_IMAGE: &str = "postgres:16.6-alpine";
/// Loopback only: Caddy is its one way in.
pub const PORT: u16 = 9400;
const ADMIN: &str = "akadmin";
const APPLICATION: &str = "commitarium";
const PROVIDER_NAME: &str = "Commitarium apps";
/// A first start migrates its database, which takes a few minutes on a
/// small server.
const READY_WITHIN: Duration = Duration::from_secs(15 * 60);
/// What the app says before installing it.
pub const MEMORY_NEEDED_GB: u64 = 3;

/// What Commitarium keeps of it: its secrets, made once.
#[derive(Clone, Deserialize, Serialize)]
struct State {
    secret_key: String,
    database_password: String,
    admin_password: String,
    /// The admin's API token, for setting it up.
    token: String,
    /// The client secret of the domain it was set up on.
    client_secret: String,
    /// Each other domain's own client secret.
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    clients: BTreeMap<String, String>,
    /// The domain it lives at, `auth.<domain>`. Unset on a server set up
    /// before several domains: then it is the first domain.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    domain: Option<String>,
    /// The application the gate signs in with is made.
    #[serde(default)]
    configured: bool,
}

/// What a client sees of it.
#[derive(Serialize)]
pub struct Status {
    installed: bool,
    running: bool,
    /// Where its admin page is, once the domain is set.
    admin_url: Option<String>,
    admin_user: &'static str,
    /// The server's memory, to compare with what authentik needs.
    memory_total_bytes: Option<u64>,
    memory_needed_gb: u64,
}

/// What the gate needs to sign in with it.
pub struct Connection {
    pub issuer: String,
    pub client_id: String,
    pub client_secret: String,
}

#[derive(Clone)]
pub struct Authentik {
    file: PathBuf,
    dir: PathBuf,
}

impl Authentik {
    pub fn new(data_dir: &Path) -> Self {
        Self {
            file: data_dir.join(FILE),
            dir: data_dir.join(DIR),
        }
    }

    fn read(&self) -> Result<Option<State>, String> {
        match fs::read(&self.file) {
            Ok(body) => serde_json::from_slice(&body)
                .map(Some)
                .map_err(|_| format!("{} is not valid", self.file.display())),
            Err(error) if error.kind() == ErrorKind::NotFound => Ok(None),
            Err(error) => Err(format!("read {}: {error}", self.file.display())),
        }
    }

    fn write(&self, state: &State) -> Result<(), String> {
        let body = serde_json::to_vec_pretty(state).map_err(|e| e.to_string())?;
        crate::publishing::write_private(&self.file, &body)
    }

    /// Set up and connected; one removed keeps its secrets for its data.
    pub fn installed(&self) -> bool {
        self.read()
            .ok()
            .flatten()
            .is_some_and(|state| state.configured)
    }

    /// The domain it lives at, when it is set up and has said so.
    pub fn home(&self) -> Option<String> {
        self.read()
            .ok()
            .flatten()
            .filter(|state| state.configured)
            .and_then(|state| state.domain)
    }

    pub fn status(&self, domain: Option<&str>) -> Status {
        let installed = self.installed();
        Status {
            installed,
            running: installed && running(),
            admin_url: domain.filter(|_| installed).map(admin_url),
            admin_user: ADMIN,
            memory_total_bytes: memory_total(),
            memory_needed_gb: MEMORY_NEEDED_GB,
        }
    }

    pub fn admin_password(&self) -> Result<String, String> {
        self.read()?
            .filter(|state| state.configured)
            .map(|state| state.admin_password)
            .ok_or_else(|| "authentik is not set up on this server".to_string())
    }

    /// Runs authentik at `auth.<home>` and makes, for each domain it
    /// serves, the application that domain's gate signs in with. `before`
    /// are the domains it served already, open to everyone until a second
    /// domain came. Safe to run again: it reuses its secrets and what it
    /// made before. Answers what `wanted`'s gate needs.
    pub fn install(
        &self,
        home: &str,
        before: &[String],
        wanted: &str,
    ) -> Result<Connection, String> {
        let mut state = match self.read()? {
            Some(state) => state,
            // Its database keeps the password it was made with.
            None if volume_exists(&format!("{PROJECT}.database")) => {
                return Err(format!(
                    "authentik's data from an earlier setup is on this server, but not its secrets \
                     ({FILE}). Restore a backup that has both, or remove the volumes \
                     {PROJECT}.database and {PROJECT}.data to start over."
                ))
            }
            None => State {
                secret_key: random(60)?,
                database_password: random(40)?,
                admin_password: random(24)?,
                token: random(48)?,
                client_secret: random(48)?,
                clients: BTreeMap::new(),
                domain: None,
                configured: false,
            },
        };
        let home = state.domain.get_or_insert_with(|| home.to_string()).clone();
        let mut domains: Vec<String> = before.to_vec();
        if !domains.iter().any(|domain| domain == wanted) {
            domains.push(wanted.to_string());
        }
        for domain in &domains {
            if *domain != home && !state.clients.contains_key(domain) {
                state.clients.insert(domain.clone(), random(48)?);
            }
        }
        self.write(&state)?;
        self.up(&state)?;
        wait_until_ready()?;
        let api = Api {
            token: &state.token,
        };
        let clients: Vec<Client> = domains
            .iter()
            .map(|domain| Client::of(&state, &home, domain, before.contains(domain)))
            .collect();
        api.configure(&clients, clients.len() >= 2)?;
        state.configured = true;
        self.write(&state)?;
        let client = Client::of(&state, &home, wanted, false);
        Ok(Connection {
            issuer: issuer(&home, wanted),
            client_id: client.slug,
            client_secret: client.secret,
        })
    }

    /// Starts it again when it should run and does not, as after a restore.
    pub fn keep_running(&self) -> Result<(), String> {
        let Some(state) = self.read()? else {
            return Ok(());
        };
        if state.configured && !running() {
            self.up(&state)?;
        }
        Ok(())
    }

    /// Stops it. Its data stays in its volumes and its secrets here, so
    /// setting it up again finds its people and groups.
    pub fn remove(&self) -> Result<(), String> {
        let compose = self.dir.join("compose.yml");
        if compose.is_file() {
            let output = docker::docker_command()
                .args(["compose", "-p", PROJECT, "-f"])
                .arg(&compose)
                .args(["down", "--remove-orphans"])
                .output()
                .map_err(|e| format!("stop authentik: {e}"))?;
            if !output.status.success() {
                return Err(format!(
                    "stop authentik: {}",
                    String::from_utf8_lossy(&output.stderr).trim()
                ));
            }
        }
        match self.read()? {
            Some(mut state) => {
                state.configured = false;
                self.write(&state)
            }
            None => Ok(()),
        }
    }

    fn up(&self, state: &State) -> Result<(), String> {
        fs::create_dir_all(&self.dir).map_err(|e| format!("create {}: {e}", self.dir.display()))?;
        let env = self.dir.join("authentik.env");
        crate::publishing::write_private(&env, environment(state).as_bytes())?;
        let compose = self.dir.join("compose.yml");
        fs::write(&compose, compose_file()).map_err(|e| format!("write authentik's setup: {e}"))?;
        let output = docker::docker_command()
            .args(["compose", "-p", PROJECT, "-f"])
            .arg(&compose)
            .args(["up", "-d", "--quiet-pull", "--remove-orphans"])
            .output()
            .map_err(|e| format!("start authentik: {e}"))?;
        if !output.status.success() {
            return Err(format!(
                "start authentik: {}",
                String::from_utf8_lossy(&output.stderr).trim()
            ));
        }
        Ok(())
    }
}

/// One domain's issuer: its application, at authentik's one address.
pub fn issuer(home: &str, domain: &str) -> String {
    format!(
        "https://{NAME}.{home}/application/o/{}/",
        slug(home, domain)
    )
}

/// Whether an issuer is one of this authentik's applications.
pub fn serves(home: &str, issuer: &str) -> bool {
    issuer.starts_with(&format!("https://{NAME}.{home}/application/o/"))
}

/// A domain's application: the one it was set up on keeps the name it had
/// before several domains, so its gate signs in as it did.
fn slug(home: &str, domain: &str) -> String {
    if home == domain {
        APPLICATION.to_string()
    } else {
        format!("{APPLICATION}-{}", domain.replace('.', "-"))
    }
}

/// The group a domain's people are in: only they may sign in there.
pub fn group_name(domain: &str) -> String {
    format!("{domain} people")
}

/// One domain's application and provider in authentik.
struct Client {
    domain: String,
    slug: String,
    /// Its provider's name, how it is found again.
    name: String,
    secret: String,
    /// It served this domain before it was bound to the domain's group, so
    /// everyone could sign in there: they all go into the group first.
    open_before: bool,
}

impl Client {
    fn of(state: &State, home: &str, domain: &str, open_before: bool) -> Self {
        let first = home == domain;
        Self {
            domain: domain.to_string(),
            slug: slug(home, domain),
            name: match first {
                true => PROVIDER_NAME.to_string(),
                false => format!("{PROVIDER_NAME} ({domain})"),
            },
            secret: match first {
                true => state.client_secret.clone(),
                false => state.clients.get(domain).cloned().unwrap_or_default(),
            },
            open_before,
        }
    }
}

/// What binding a domain's application to its group still needs.
#[derive(Debug, PartialEq)]
enum Binding {
    Done,
    Bind,
    /// It was open to everyone: they all go into the group, then it is bound,
    /// so no one loses access.
    BringEveryoneThenBind,
}

fn binding_needed(bindings: &[Value], group: &Value, open_before: bool) -> Binding {
    if bindings.iter().any(|binding| binding["group"] == *group) {
        Binding::Done
    } else if open_before && bindings.is_empty() {
        Binding::BringEveryoneThenBind
    } else {
        Binding::Bind
    }
}

fn binding_body(application: &Value, group: &Value) -> Value {
    json!({
        "target": application,
        "group": group,
        "order": 0,
        "enabled": true,
        "negate": false,
        "timeout": 30,
    })
}

/// The people among authentik's users: not its service accounts, such as
/// its outposts', which never sign in to an app.
fn people(users: &[Value]) -> Vec<Value> {
    users
        .iter()
        .filter(|user| {
            !matches!(
                user["type"].as_str(),
                Some("service_account" | "internal_service_account")
            )
        })
        .map(|user| user["pk"].clone())
        .collect()
}

/// What the gate asks authentik for: offline_access brings refresh tokens,
/// with which the gate checks each hour that a person may still sign in.
pub const SCOPES: &str = "openid email profile offline_access";

/// Where authentik ends a person's session, as its discovery names it.
pub fn end_session(issuer: &str) -> String {
    format!("{}/end-session/", issuer.trim_end_matches('/'))
}

fn admin_url(domain: &str) -> String {
    format!("https://{NAME}.{domain}/if/admin/")
}

/// Secrets reach it through a private file, never a command line. The
/// bootstrap ones make its admin and token on its first start only.
fn environment(state: &State) -> String {
    [
        ("AUTHENTIK_SECRET_KEY", state.secret_key.as_str()),
        ("AUTHENTIK_POSTGRESQL__HOST", "postgresql"),
        ("AUTHENTIK_POSTGRESQL__USER", "authentik"),
        ("AUTHENTIK_POSTGRESQL__NAME", "authentik"),
        ("AUTHENTIK_POSTGRESQL__PASSWORD", &state.database_password),
        ("POSTGRES_USER", "authentik"),
        ("POSTGRES_DB", "authentik"),
        ("POSTGRES_PASSWORD", &state.database_password),
        ("AUTHENTIK_BOOTSTRAP_PASSWORD", &state.admin_password),
        ("AUTHENTIK_BOOTSTRAP_TOKEN", &state.token),
        ("AUTHENTIK_BOOTSTRAP_EMAIL", "admin@localhost"),
        ("AUTHENTIK_ERROR_REPORTING__ENABLED", "false"),
        ("AUTHENTIK_DISABLE_UPDATE_CHECK", "true"),
    ]
    .iter()
    .map(|(name, value)| format!("{name}={value}\n"))
    .collect()
}

/// Its Compose project. Volumes are named `<project>.<volume>`, as deployed
/// apps' are, so backups find and pause them the same way.
fn compose_file() -> String {
    let logging = "    logging: {driver: json-file, options: {max-size: 10m, max-file: \"3\"}}\n";
    format!(
        "name: {PROJECT}\n\
         services:\n\
         \x20 postgresql:\n\
         \x20   image: {DATABASE_IMAGE}\n\
         \x20   restart: unless-stopped\n\
         \x20   env_file: authentik.env\n\
         \x20   healthcheck: {{test: [\"CMD-SHELL\", \"pg_isready -d authentik -U authentik\"], interval: 10s, retries: 30}}\n\
         \x20   volumes: [database:/var/lib/postgresql/data]\n\
         {logging}\
         \x20 server:\n\
         \x20   image: {IMAGE}\n\
         \x20   command: server\n\
         \x20   restart: unless-stopped\n\
         \x20   env_file: authentik.env\n\
         \x20   ports: [\"127.0.0.1:{PORT}:9000\"]\n\
         \x20   volumes: [data:/data]\n\
         \x20   depends_on: {{postgresql: {{condition: service_healthy}}}}\n\
         {logging}\
         \x20 worker:\n\
         \x20   image: {IMAGE}\n\
         \x20   command: worker\n\
         \x20   restart: unless-stopped\n\
         \x20   env_file: authentik.env\n\
         \x20   volumes: [data:/data]\n\
         \x20   depends_on: {{postgresql: {{condition: service_healthy}}}}\n\
         {logging}\
         volumes:\n\
         \x20 database: {{name: {PROJECT}.database}}\n\
         \x20 data: {{name: {PROJECT}.data}}\n"
    )
}

fn running() -> bool {
    docker::docker_command()
        .args([
            "ps",
            "--quiet",
            "--filter",
            &format!("label=com.docker.compose.project={PROJECT}"),
            "--filter",
            "label=com.docker.compose.service=server",
        ])
        .output()
        .is_ok_and(|output| output.status.success() && !output.stdout.trim_ascii().is_empty())
}

fn volume_exists(name: &str) -> bool {
    docker::docker_command()
        .args(["volume", "inspect", name])
        .output()
        .is_ok_and(|output| output.status.success())
}

fn wait_until_ready() -> Result<(), String> {
    let started = Instant::now();
    let url = format!("http://127.0.0.1:{PORT}/-/health/ready/");
    while started.elapsed() < READY_WITHIN {
        let ready = block_on(async {
            reqwest::Client::new()
                .get(&url)
                .timeout(Duration::from_secs(5))
                .send()
                .await
                .is_ok_and(|response| response.status().is_success())
        });
        if ready {
            return Ok(());
        }
        std::thread::sleep(Duration::from_secs(5));
    }
    Err("authentik did not start within 15 minutes: the server may be short of memory".into())
}

/// Its API, as its admin.
struct Api<'a> {
    token: &'a str,
}

impl Api<'_> {
    fn call(
        &self,
        method: reqwest::Method,
        path: &str,
        body: Option<Value>,
    ) -> Result<Value, String> {
        let url = format!("http://127.0.0.1:{PORT}/api/v3/{path}");
        // Some answers have no body, such as adding a person to a group.
        block_on(async {
            let mut request = reqwest::Client::new()
                .request(method, &url)
                .bearer_auth(self.token)
                .timeout(Duration::from_secs(30));
            if let Some(body) = body {
                request = request.json(&body);
            }
            let response = request
                .send()
                .await
                .map_err(|e| format!("reach authentik: {e}"))?;
            let status = response.status();
            let text = response.text().await.unwrap_or_default();
            if !status.is_success() {
                return Err(format!("authentik answered {status} to {path}: {text}"));
            }
            if text.trim().is_empty() {
                return Ok(Value::Null);
            }
            serde_json::from_str(&text)
                .map_err(|_| format!("authentik answered {path} with no JSON"))
        })
    }

    fn get(&self, path: &str) -> Result<Value, String> {
        // Its token is ready a moment after its health check is.
        let mut last = String::new();
        for _ in 0..24 {
            match self.call(reqwest::Method::GET, path, None) {
                Ok(value) => return Ok(value),
                Err(error) => last = error,
            }
            std::thread::sleep(Duration::from_secs(5));
        }
        Err(last)
    }

    fn first(&self, path: &str) -> Result<Option<Value>, String> {
        Ok(self.get(path)?["results"].get(0).cloned())
    }

    /// One of the defaults its worker makes once it is up, which may be a
    /// few minutes after its first start.
    fn pk_of(&self, path: &str, what: &str) -> Result<Value, String> {
        for _ in 0..60 {
            if let Some(found) = self.first(path)? {
                return Ok(found["pk"].clone());
            }
            std::thread::sleep(Duration::from_secs(5));
        }
        Err(format!("authentik has no {what}"))
    }

    /// authentik's own email mapping calls every email unverified, which the
    /// gate refuses. Here its admins set people's emails, so they are.
    fn email_mapping(&self) -> Result<Value, String> {
        const NAME: &str = "Commitarium: email";
        let mapping = json!({
            "name": NAME,
            "scope_name": "email",
            "expression": "return {\"email\": request.user.email, \"email_verified\": True}",
        });
        match self.first("propertymappings/provider/scope/?name=Commitarium%3A%20email")? {
            Some(found) => Ok(found["pk"].clone()),
            None => Ok(self.call(
                reqwest::Method::POST,
                "propertymappings/provider/scope/",
                Some(mapping),
            )?["pk"]
                .clone()),
        }
    }

    /// Makes, or brings up to date, each domain's provider and application.
    /// With two or more domains, each application is bound to its domain's
    /// group.
    fn configure(&self, clients: &[Client], bind: bool) -> Result<(), String> {
        let authorization = self.pk_of(
            "flows/instances/?slug=default-provider-authorization-implicit-consent",
            "authorization flow",
        )?;
        // Signing out of the apps ends the authentik session too: the
        // provider-only flow would leave it, and the gate would sign the
        // person straight back in.
        let invalidation = self.pk_of(
            "flows/instances/?slug=default-invalidation-flow",
            "invalidation flow",
        )?;
        let signing_key = self.pk_of(
            "crypto/certificatekeypairs/?name=authentik%20Self-signed%20Certificate",
            "signing key",
        )?;
        let mut mappings: Vec<Value> = self
            .get("propertymappings/provider/scope/?page_size=100")?["results"]
            .as_array()
            .cloned()
            .unwrap_or_default()
            .into_iter()
            .filter(|mapping| {
                // offline_access: refresh tokens, for the gate's hourly check.
                ["openid", "profile", "offline_access"].iter().any(|scope| {
                    mapping["managed"] == format!("goauthentik.io/providers/oauth2/scope-{scope}")
                })
            })
            .map(|mapping| mapping["pk"].clone())
            .collect();
        mappings.push(self.email_mapping()?);
        // Its sign-in lasts as long as the gate's: a session kept for the
        // browser's life would sign a person straight back in.
        if let Some(login) = self.first("stages/user_login/?name=default-authentication-login")? {
            self.call(
                reqwest::Method::PATCH,
                &format!(
                    "stages/user_login/{}/",
                    login["pk"].as_str().unwrap_or_default()
                ),
                Some(json!({
                    "session_duration": format!("hours={}", crate::sign_in::SIGN_IN_LASTS_HOURS),
                })),
            )?;
        }
        let shared = Shared {
            authorization,
            invalidation,
            signing_key,
            mappings,
        };
        for client in clients {
            let application = self.application(&shared, client)?;
            if bind {
                self.bind_to_group(&application, client)?;
            }
        }
        Ok(())
    }

    /// One domain's provider and application; answers the application's pk.
    fn application(&self, shared: &Shared, client: &Client) -> Result<Value, String> {
        let provider = provider_body(shared, client);
        let provider_pk = match self.first(&format!(
            "providers/oauth2/?name={}",
            crate::sign_in::encode(&client.name)
        ))? {
            Some(found) => {
                let pk = found["pk"].clone();
                self.call(
                    reqwest::Method::PUT,
                    &format!("providers/oauth2/{pk}/"),
                    Some(provider),
                )?;
                pk
            }
            None => {
                self.call(reqwest::Method::POST, "providers/oauth2/", Some(provider))?["pk"].clone()
            }
        };
        match self.first(&format!("core/applications/?slug={}", client.slug))? {
            Some(found) => Ok(found["pk"].clone()),
            None => Ok(self.call(
                reqwest::Method::POST,
                "core/applications/",
                Some(json!({"name": client.name, "slug": client.slug, "provider": provider_pk})),
            )?["pk"]
                .clone()),
        }
    }

    /// Binds a domain's application to its group, making the group first.
    /// One that was open to everyone brings them all into the group before
    /// it is bound. People added later are the owner's to add, in
    /// authentik's admin.
    fn bind_to_group(&self, application: &Value, client: &Client) -> Result<(), String> {
        let name = group_name(&client.domain);
        let group = match self.first(&format!(
            "core/groups/?name={}",
            crate::sign_in::encode(&name)
        ))? {
            Some(found) => found["pk"].clone(),
            None => self.call(
                reqwest::Method::POST,
                "core/groups/",
                Some(json!({"name": name, "is_superuser": false})),
            )?["pk"]
                .clone(),
        };
        let target = application.as_str().unwrap_or_default();
        let bindings = self.get(&format!("policies/bindings/?target={target}"))?["results"]
            .as_array()
            .cloned()
            .unwrap_or_default();
        match binding_needed(&bindings, &group, client.open_before) {
            Binding::Done => return Ok(()),
            Binding::Bind => {}
            Binding::BringEveryoneThenBind => {
                let group_pk = group.as_str().unwrap_or_default();
                for person in self.everyone()? {
                    self.call(
                        reqwest::Method::POST,
                        &format!("core/groups/{group_pk}/add_user/"),
                        Some(json!({"pk": person})),
                    )?;
                }
            }
        }
        self.call(
            reqwest::Method::POST,
            "policies/bindings/",
            Some(binding_body(application, &group)),
        )?;
        Ok(())
    }

    /// Every person in authentik, page by page.
    fn everyone(&self) -> Result<Vec<Value>, String> {
        let mut found = Vec::new();
        let mut page = 1;
        loop {
            let answer = self.get(&format!("core/users/?page_size=100&page={page}"))?;
            let users = answer["results"].as_array().cloned().unwrap_or_default();
            found.extend(people(&users));
            match answer["pagination"]["next"].as_u64() {
                Some(next) if next > page => page = next,
                _ => return Ok(found),
            }
        }
    }
}

/// What every domain's provider shares.
struct Shared {
    authorization: Value,
    invalidation: Value,
    signing_key: Value,
    mappings: Vec<Value>,
}

/// A domain's provider: its own client, sending people back only to its
/// own sign-in address.
fn provider_body(shared: &Shared, client: &Client) -> Value {
    json!({
        "name": client.name,
        "authorization_flow": shared.authorization,
        "invalidation_flow": shared.invalidation,
        "client_type": "confidential",
        "client_id": client.slug,
        "client_secret": client.secret,
        "redirect_uris": [{
            "matching_mode": "strict",
            "url": format!("https://{}.{}/oauth2/callback", crate::sign_in::NAME, client.domain),
        }],
        "signing_key": shared.signing_key,
        "property_mappings": shared.mappings,
    })
}

/// Letters and digits: they need no quoting in its environment file.
fn random(length: usize) -> Result<String, String> {
    const ALPHABET: &[u8] = b"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";
    let mut bytes = vec![0u8; length];
    getrandom::fill(&mut bytes).map_err(|e| format!("make a secret: {e}"))?;
    Ok(bytes
        .iter()
        .map(|byte| ALPHABET[*byte as usize % ALPHABET.len()] as char)
        .collect())
}

/// The machine's memory, where the system says (Linux).
fn memory_total() -> Option<u64> {
    let meminfo = fs::read_to_string("/proc/meminfo").ok()?;
    let line = meminfo.lines().find(|line| line.starts_with("MemTotal:"))?;
    let kilobytes: u64 = line.split_whitespace().nth(1)?.parse().ok()?;
    Some(kilobytes * 1024)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn its_secrets_stay_in_its_private_file() {
        let state = State {
            secret_key: "key".into(),
            database_password: "dbpass".into(),
            admin_password: "admin".into(),
            token: "token".into(),
            client_secret: "client".into(),
            clients: BTreeMap::from([("example.org".into(), "other".into())]),
            domain: Some("example.com".into()),
            configured: false,
        };
        let compose = compose_file();
        for secret in ["key", "dbpass", "admin", "token", "client", "other"] {
            assert!(!compose.contains(&format!("={secret}")));
        }
        assert!(compose.contains("127.0.0.1:9400:9000"));
        assert!(compose.contains("commitarium-authentik.database"));
        let env = environment(&state);
        assert!(env.contains("AUTHENTIK_POSTGRESQL__PASSWORD=dbpass\n"));
        assert!(env.contains("POSTGRES_PASSWORD=dbpass\n"));
        assert!(env.contains("AUTHENTIK_BOOTSTRAP_TOKEN=token\n"));
        assert_eq!(
            issuer("example.com", "example.com"),
            "https://auth.example.com/application/o/commitarium/"
        );
    }

    #[test]
    fn each_domain_gets_its_own_client_and_the_first_keeps_its_own() {
        let state = State {
            secret_key: "key".into(),
            database_password: "dbpass".into(),
            admin_password: "admin".into(),
            token: "token".into(),
            client_secret: "first".into(),
            clients: BTreeMap::from([("example.org".into(), "second".into())]),
            domain: Some("example.com".into()),
            configured: true,
        };
        let first = Client::of(&state, "example.com", "example.com", true);
        assert_eq!(first.slug, "commitarium");
        assert_eq!(first.name, "Commitarium apps");
        assert_eq!(first.secret, "first");
        let second = Client::of(&state, "example.com", "example.org", false);
        assert_eq!(second.slug, "commitarium-example-org");
        assert_eq!(second.name, "Commitarium apps (example.org)");
        assert_eq!(second.secret, "second");
        // One authentik, at the domain it was set up on, for both.
        assert_eq!(
            issuer("example.com", "example.org"),
            "https://auth.example.com/application/o/commitarium-example-org/"
        );
        assert!(serves("example.com", &issuer("example.com", "example.org")));
        assert!(!serves(
            "example.org",
            &issuer("example.com", "example.org")
        ));
        let shared = Shared {
            authorization: json!("flow"),
            invalidation: json!("end"),
            signing_key: json!("key"),
            mappings: vec![json!("email")],
        };
        let body = provider_body(&shared, &second);
        assert_eq!(body["client_id"], "commitarium-example-org");
        assert_eq!(body["client_secret"], "second");
        assert_eq!(
            body["redirect_uris"][0]["url"],
            "https://sign-in.example.org/oauth2/callback"
        );
        assert_eq!(group_name("example.org"), "example.org people");
    }

    #[test]
    fn a_domain_open_to_everyone_brings_them_into_its_group_before_it_is_bound() {
        let group = json!("group-pk");
        // The first domain served everyone: keep them, then bind.
        assert_eq!(
            binding_needed(&[], &group, true),
            Binding::BringEveryoneThenBind
        );
        // A new domain starts with an empty group.
        assert_eq!(binding_needed(&[], &group, false), Binding::Bind);
        // Bound already: nothing more, and no one removed is brought back.
        let bound = [json!({"group": "group-pk", "target": "app"})];
        assert_eq!(binding_needed(&bound, &group, true), Binding::Done);
        // The owner's own bindings stay, and the group joins them.
        let theirs = [json!({"group": "other", "target": "app"})];
        assert_eq!(binding_needed(&theirs, &group, true), Binding::Bind);
        let body = binding_body(&json!("app"), &group);
        assert_eq!(body["target"], "app");
        assert_eq!(body["group"], "group-pk");
        assert_eq!(body["enabled"], true);
        let users = [
            json!({"pk": 1, "type": "internal"}),
            json!({"pk": 2, "type": "external"}),
            json!({"pk": 3, "type": "service_account"}),
            json!({"pk": 4, "type": "internal_service_account"}),
        ];
        assert_eq!(people(&users), [json!(1), json!(2)]);
    }

    #[test]
    fn secrets_are_letters_and_digits() {
        let secret = random(40).unwrap();
        assert_eq!(secret.len(), 40);
        assert!(secret.bytes().all(|b| b.is_ascii_alphanumeric()));
    }

    /// Installs authentik on this computer's Docker and checks it answers as
    /// the provider: `cargo test -- --ignored authentik_installs`.
    #[test]
    #[ignore]
    fn authentik_installs_and_answers_as_a_provider() {
        let clean = || {
            let _ = docker::docker_command()
                .args(["compose", "-p", PROJECT, "down", "-v"])
                .output();
            let _ = docker::docker_command()
                .args([
                    "volume",
                    "rm",
                    "commitarium-authentik.database",
                    "commitarium-authentik.data",
                ])
                .output();
        };
        clean();
        let data = tempfile::tempdir().unwrap();
        let authentik = Authentik::new(data.path());
        let connection = authentik
            .install("example.test", &[], "example.test")
            .unwrap();
        assert_eq!(connection.issuer, issuer("example.test", "example.test"));
        // Again: it reuses what it made.
        let first = ["example.test".to_string()];
        authentik
            .install("example.test", &first, "example.test")
            .unwrap();
        // A second domain: its own application, and both bound to groups.
        let second = authentik
            .install("example.test", &first, "other.test")
            .unwrap();
        assert_ne!(second.client_id, connection.client_id);
        assert_ne!(second.client_secret, connection.client_secret);
        let discovery: Value = block_on(async {
            reqwest::Client::new()
                .get(format!(
                    "http://127.0.0.1:{PORT}/application/o/{APPLICATION}/.well-known/openid-configuration"
                ))
                .header("Host", "auth.example.test")
                .header("X-Forwarded-Proto", "https")
                .send()
                .await
                .unwrap()
                .json()
                .await
                .unwrap()
        });
        assert_eq!(discovery["issuer"], connection.issuer);
        authentik.remove().unwrap();
        assert!(!authentik.installed());
        // Its data without its secrets is refused, not half-started.
        let lost = tempfile::tempdir().unwrap();
        let error = Authentik::new(lost.path())
            .install("example.test", &[], "example.test")
            .err()
            .unwrap();
        assert!(error.contains("earlier setup"), "{error}");
        clean();
    }
}
