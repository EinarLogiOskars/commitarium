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

use crate::docker;
use crate::runtime::block_on;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
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
const CLIENT_ID: &str = "commitarium";
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
    client_secret: String,
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

    /// Runs authentik and makes the application the gate signs in with. Safe
    /// to run again: it reuses its secrets and what it made before.
    pub fn install(&self, domain: &str) -> Result<Connection, String> {
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
                configured: false,
            },
        };
        self.write(&state)?;
        self.up(&state)?;
        wait_until_ready()?;
        let api = Api {
            token: &state.token,
        };
        api.configure(domain, &state.client_secret)?;
        state.configured = true;
        self.write(&state)?;
        Ok(Connection {
            issuer: issuer(domain),
            client_id: CLIENT_ID.to_string(),
            client_secret: state.client_secret,
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

pub fn issuer(domain: &str) -> String {
    format!("https://{NAME}.{domain}/application/o/{APPLICATION}/")
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

    /// Makes, or brings up to date, the provider and application the gate
    /// signs in with.
    fn configure(&self, domain: &str, client_secret: &str) -> Result<(), String> {
        let authorization = self.pk_of(
            "flows/instances/?slug=default-provider-authorization-implicit-consent",
            "authorization flow",
        )?;
        let invalidation = self.pk_of(
            "flows/instances/?slug=default-provider-invalidation-flow",
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
                ["openid", "profile"].iter().any(|scope| {
                    mapping["managed"] == format!("goauthentik.io/providers/oauth2/scope-{scope}")
                })
            })
            .map(|mapping| mapping["pk"].clone())
            .collect();
        mappings.push(self.email_mapping()?);
        let provider = json!({
            "name": PROVIDER_NAME,
            "authorization_flow": authorization,
            "invalidation_flow": invalidation,
            "client_type": "confidential",
            "client_id": CLIENT_ID,
            "client_secret": client_secret,
            "redirect_uris": [{
                "matching_mode": "strict",
                "url": format!("https://{}.{domain}/oauth2/callback", crate::sign_in::NAME),
            }],
            "signing_key": signing_key,
            "property_mappings": mappings,
        });
        let provider_pk = match self.first("providers/oauth2/?name=Commitarium%20apps")? {
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
        if self
            .first(&format!("core/applications/?slug={APPLICATION}"))?
            .is_none()
        {
            self.call(
                reqwest::Method::POST,
                "core/applications/",
                Some(json!({"name": PROVIDER_NAME, "slug": APPLICATION, "provider": provider_pk})),
            )?;
        }
        Ok(())
    }
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
            configured: false,
        };
        let compose = compose_file();
        for secret in ["key", "dbpass", "admin", "token", "client"] {
            assert!(!compose.contains(&format!("={secret}")));
        }
        assert!(compose.contains("127.0.0.1:9400:9000"));
        assert!(compose.contains("commitarium-authentik.database"));
        let env = environment(&state);
        assert!(env.contains("AUTHENTIK_POSTGRESQL__PASSWORD=dbpass\n"));
        assert!(env.contains("POSTGRES_PASSWORD=dbpass\n"));
        assert!(env.contains("AUTHENTIK_BOOTSTRAP_TOKEN=token\n"));
        assert_eq!(
            issuer("example.com"),
            "https://auth.example.com/application/o/commitarium/"
        );
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
        let connection = authentik.install("example.test").unwrap();
        assert_eq!(connection.issuer, issuer("example.test"));
        // Again: it reuses what it made.
        authentik.install("example.test").unwrap();
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
            .install("example.test")
            .err()
            .unwrap();
        assert!(error.contains("earlier setup"), "{error}");
        clean();
    }
}
