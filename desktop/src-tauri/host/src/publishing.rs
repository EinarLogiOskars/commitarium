//! Publishing (PUBLISHING_PLAN.md, D2): a server's base domain, and an
//! address for each deployed app, served by Caddy. Caddy is the only public
//! door. It runs in a container on the host's network, so it reaches the
//! deployments' loopback ports, and its admin API is a socket inside that
//! container, never a port. The host writes Caddy's whole configuration from
//! the settings and the running deployments, and reloads it when it changes.

use crate::authentik::{self, Authentik};
use crate::deployments::Deployments;
use crate::docker;
use crate::sign_in::{self, Provider};
use base64::Engine as _;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::collections::BTreeMap;
use std::fs;
use std::io::ErrorKind;
use std::net::{IpAddr, ToSocketAddrs};
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

const FILE: &str = "publishing.json";
const CADDY_DIR: &str = "caddy";
const CADDY_CONFIG: &str = "caddy.json";
const CADDY_CONTAINER: &str = "commitarium-caddy";
const CADDY_IMAGE: &str = "caddy:2.9.1";
const CADDY_DATA: &str = "commitarium-caddy-data";
/// Inside the container only: nothing on the host can reach it.
const CADDY_ADMIN: &str = "unix//tmp/caddy-admin.sock";
/// How often the routes are checked against the running deployments.
const SYNC_EVERY: Duration = Duration::from_secs(5);
/// Names that read as the server's own, or as something else's.
const RESERVED: &[&str] = &[
    "www",
    "mail",
    "smtp",
    "imap",
    "ftp",
    "api",
    "admin",
    "app",
    "apps",
    "commitarium",
    "forgejo",
    "git",
    "host",
    "status",
    "ns1",
    "ns2",
    sign_in::NAME,
    authentik::NAME,
];

#[derive(Clone, Copy, Debug, Default, Deserialize, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Access {
    /// Only people with the password: the default.
    #[default]
    Private,
    Public,
    /// People sign in with their company account (COMPANY_SIGN_IN_PLAN.md).
    Company,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct AppRoute {
    name: String,
    access: Access,
    /// bcrypt, only for a private app: what Caddy checks against.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    password_hash: Option<String>,
    /// The password itself, so the owner can look it up and share it again
    /// instead of changing one others use. The file is the service user's
    /// alone, and only paired devices can ask for it.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    password: Option<String>,
    /// For company sign-in: the groups that may open it; none means anyone
    /// at the company.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    groups: Vec<String>,
}

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
struct Settings {
    domain: Option<String>,
    #[serde(default)]
    apps: BTreeMap<String, AppRoute>,
    /// The company's sign-in provider, once connected.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    sign_in: Option<Provider>,
}

/// A published app as a client sees it.
#[derive(Serialize)]
pub struct PublishedApp {
    project_id: String,
    name: String,
    access: Access,
    url: String,
    /// Whether its deployment runs, so the address answers with the app.
    live: bool,
    groups: Vec<String>,
}

#[derive(Serialize)]
pub struct Overview {
    domain: Option<String>,
    /// The address the wildcard record should point at.
    server_address: Option<String>,
    apps: Vec<PublishedApp>,
    /// The company's sign-in provider, when one is connected.
    sign_in: Option<sign_in::Connected>,
    /// authentik on this server, and what it needs.
    authentik: authentik::Status,
}

/// What publishing an app answers: its address.
#[derive(Serialize)]
pub struct Published {
    url: String,
}

#[derive(Clone)]
pub struct Publishing {
    file: PathBuf,
    caddy_dir: PathBuf,
    authentik: Authentik,
    /// Orders changes to the file, and holds the configuration last loaded.
    applied: Arc<Mutex<Option<String>>>,
}

impl Publishing {
    pub fn new(data_dir: &Path) -> Self {
        Self {
            file: data_dir.join(FILE),
            caddy_dir: data_dir.join(CADDY_DIR),
            authentik: Authentik::new(data_dir),
            applied: Arc::default(),
        }
    }

    fn read(&self) -> Result<Settings, String> {
        match fs::read(&self.file) {
            Ok(body) => serde_json::from_slice(&body)
                .map_err(|_| format!("{} is not valid", self.file.display())),
            Err(error) if error.kind() == ErrorKind::NotFound => Ok(Settings::default()),
            Err(error) => Err(format!("read {}: {error}", self.file.display())),
        }
    }

    fn write(&self, settings: &Settings) -> Result<(), String> {
        let body = serde_json::to_vec_pretty(settings).map_err(|e| e.to_string())?;
        write_private(&self.file, &body)
    }

    pub fn overview(&self, deployments: &Deployments) -> Result<Overview, String> {
        let settings = self.read()?;
        let running = deployments.running_ports();
        Ok(Overview {
            server_address: server_address().map(|ip| ip.to_string()),
            apps: settings
                .apps
                .iter()
                .map(|(project_id, route)| PublishedApp {
                    project_id: project_id.clone(),
                    name: route.name.clone(),
                    access: route.access,
                    url: settings
                        .domain
                        .as_deref()
                        .map(|domain| address(&route.name, domain))
                        .unwrap_or_default(),
                    live: running.contains_key(project_id),
                    groups: route.groups.clone(),
                })
                .collect(),
            sign_in: settings
                .sign_in
                .as_ref()
                .map(|provider| provider.shown(settings.domain.as_deref())),
            authentik: self.authentik.status(settings.domain.as_deref()),
            domain: settings.domain,
        })
    }

    /// Sets the base domain once its wildcard record points here.
    pub fn set_domain(&self, domain: &str) -> Result<(), String> {
        let domain = normalized_domain(domain)?;
        let here = server_address().ok_or("this server's address is unknown")?;
        let probe = format!("commitarium-check.{domain}");
        if !resolves_to(&probe, here) {
            return Err(format!(
                "*.{domain} does not point at this server yet. At your DNS provider, add an \
                 A record for *.{domain} with the value {here}, then try again. New records \
                 can take a few minutes."
            ));
        }
        let _held = self
            .applied
            .lock()
            .map_err(|_| "publishing is unavailable")?;
        let mut settings = self.read()?;
        settings.domain = Some(domain);
        self.write(&settings)
    }

    /// Gives a deployed app its address. A private app needs a password the
    /// first time; after that it keeps its own unless a new one is given.
    pub fn publish(
        &self,
        project_id: &str,
        name: &str,
        access: Access,
        password: Option<&str>,
    ) -> Result<Published, String> {
        let name = valid_name(name)?;
        let _held = self
            .applied
            .lock()
            .map_err(|_| "publishing is unavailable")?;
        let mut settings = self.read()?;
        let domain = settings
            .domain
            .clone()
            .ok_or("set this server's domain in Settings first")?;
        if settings
            .apps
            .iter()
            .any(|(other, route)| other != project_id && route.name == name)
        {
            return Err(format!("{name} is already another app's address"));
        }
        let here = server_address().ok_or("this server's address is unknown")?;
        let url = address(&name, &domain);
        if !resolves_to(&format!("{name}.{domain}"), here) {
            return Err(format!(
                "{name}.{domain} points somewhere else, so it is used elsewhere. Choose \
                 another name."
            ));
        }
        let previous = settings
            .apps
            .get(project_id)
            .and_then(|route| Some((route.password_hash.clone()?, route.password.clone())));
        // A public app keeps the password it had, so making it private again
        // needs no new one; Caddy asks for it only while the app is private.
        let (password_hash, password) = match (access, password, previous) {
            (_, Some(password), _) => (Some(hashed(password)?), Some(password.to_string())),
            (_, None, Some((hash, kept))) => (Some(hash), kept),
            (Access::Public | Access::Company, None, None) => (None, None),
            (Access::Private, None, None) => {
                return Err("choose a password for who may open it".into())
            }
        };
        if access == Access::Company && settings.sign_in.is_none() {
            return Err(NO_PROVIDER.into());
        }
        let groups = settings
            .apps
            .get(project_id)
            .map(|route| route.groups.clone())
            .unwrap_or_default();
        settings.apps.insert(
            project_id.to_string(),
            AppRoute {
                name,
                access,
                password_hash,
                password,
                groups,
            },
        );
        self.write(&settings)?;
        Ok(Published { url })
    }

    /// Changes a private app's password.
    pub fn set_app_password(&self, project_id: &str, password: &str) -> Result<(), String> {
        let hash = hashed(password)?;
        let _held = self
            .applied
            .lock()
            .map_err(|_| "publishing is unavailable")?;
        let mut settings = self.read()?;
        let route = settings
            .apps
            .get_mut(project_id)
            .ok_or("this app is not published")?;
        route.password_hash = Some(hash);
        route.password = Some(password.to_string());
        self.write(&settings)
    }

    /// Who can open an app: anyone, or only with its password. Making it
    /// private again uses the password it had.
    pub fn set_access(&self, project_id: &str, access: Access) -> Result<(), String> {
        let _held = self
            .applied
            .lock()
            .map_err(|_| "publishing is unavailable")?;
        let mut settings = self.read()?;
        let route = settings
            .apps
            .get_mut(project_id)
            .ok_or("this app is not published")?;
        if access == Access::Private && route.password_hash.is_none() {
            return Err("choose a password for who may open it".into());
        }
        route.access = access;
        if access == Access::Company && settings.sign_in.is_none() {
            return Err(NO_PROVIDER.into());
        }
        self.write(&settings)
    }

    /// The groups that may open a company app; none means anyone at the
    /// company.
    pub fn set_groups(&self, project_id: &str, groups: &[String]) -> Result<(), String> {
        let mut groups = groups
            .iter()
            .map(|group| sign_in::valid_group(group))
            .collect::<Result<Vec<_>, _>>()?;
        groups.sort();
        groups.dedup();
        let _held = self
            .applied
            .lock()
            .map_err(|_| "publishing is unavailable")?;
        let mut settings = self.read()?;
        let route = settings
            .apps
            .get_mut(project_id)
            .ok_or("this app is not published")?;
        route.groups = groups;
        self.write(&settings)
    }

    /// Connects the company's sign-in provider, once it answers as one. A
    /// changed provider keeps the cookie secret, so no one is signed out
    /// for nothing.
    pub fn connect_sign_in(&self, asked: sign_in::Connect) -> Result<sign_in::Connected, String> {
        let kept = self.read()?.sign_in.map(|provider| provider.cookie_secret);
        // Checked before the lock: it waits on the provider.
        let provider = Provider::connect(asked, kept)?;
        let _held = self
            .applied
            .lock()
            .map_err(|_| "publishing is unavailable")?;
        let mut settings = self.read()?;
        let shown = provider.shown(settings.domain.as_deref());
        settings.sign_in = Some(provider);
        self.write(&settings)?;
        Ok(shown)
    }

    /// Sets authentik up on this server and connects it as the company's
    /// provider. It takes minutes: images, then its first start.
    pub fn install_authentik(&self) -> Result<sign_in::Connected, String> {
        let settings = self.read()?;
        let domain = settings
            .domain
            .clone()
            .ok_or("set this server's domain in Settings first")?;
        if settings.sign_in.is_some() && !self.authentik.installed() {
            return Err("another provider is connected: disconnect it first".into());
        }
        let connection = self.authentik.install(&domain)?;
        let _held = self
            .applied
            .lock()
            .map_err(|_| "publishing is unavailable")?;
        let mut settings = self.read()?;
        // Its own: no need to ask it whether it answers, before Caddy has
        // its address.
        let cookie_secret = match settings.sign_in.take() {
            Some(provider) => provider.cookie_secret,
            None => sign_in::new_cookie_secret()?,
        };
        let provider = Provider {
            issuer: connection.issuer,
            client_id: connection.client_id,
            client_secret: connection.client_secret,
            scopes: None,
            groups_claim: None,
            cookie_secret,
        };
        let shown = provider.shown(Some(&domain));
        settings.sign_in = Some(provider);
        self.write(&settings)?;
        Ok(shown)
    }

    /// authentik's admin password, for its admin page.
    pub fn authentik_password(&self) -> Result<String, String> {
        self.authentik.admin_password()
    }

    /// Disconnects the provider, once no app uses it. authentik on this
    /// server stops with it; its data stays for setting it up again.
    pub fn disconnect_sign_in(&self) -> Result<(), String> {
        let _held = self
            .applied
            .lock()
            .map_err(|_| "publishing is unavailable")?;
        let mut settings = self.read()?;
        let using: Vec<&str> = settings
            .apps
            .values()
            .filter(|route| route.access == Access::Company)
            .map(|route| route.name.as_str())
            .collect();
        if !using.is_empty() {
            return Err(format!(
                "{} still use company sign-in: choose who can open them first",
                using.join(", ")
            ));
        }
        settings.sign_in = None;
        self.write(&settings)?;
        if self.authentik.installed() {
            self.authentik.remove()?;
        }
        Ok(())
    }

    /// A private app's password, for its owner to share again. `None` for
    /// one set before passwords were kept.
    pub fn app_password(&self, project_id: &str) -> Result<Option<String>, String> {
        let settings = self.read()?;
        let route = settings
            .apps
            .get(project_id)
            .ok_or("this app is not published")?;
        Ok(route.password.clone())
    }

    pub fn unpublish(&self, project_id: &str) -> Result<(), String> {
        let _held = self
            .applied
            .lock()
            .map_err(|_| "publishing is unavailable")?;
        let mut settings = self.read()?;
        settings.apps.remove(project_id);
        self.write(&settings)
    }

    /// Keeps Caddy's routes in step with the settings and the running
    /// deployments, for as long as the host runs.
    pub fn keep_in_sync(self, deployments: Deployments) {
        thread::spawn(move || loop {
            if let Err(error) = self.sync(&deployments) {
                eprintln!("[publishing] {error}");
            }
            thread::sleep(SYNC_EVERY);
        });
    }

    fn sync(&self, deployments: &Deployments) -> Result<(), String> {
        let mut applied = self
            .applied
            .lock()
            .map_err(|_| "publishing is unavailable")?;
        let settings = self.read()?;
        let Some(domain) = settings.domain.as_deref() else {
            return Ok(());
        };
        sign_in::sync(settings.sign_in.as_ref(), domain)?;
        self.authentik.keep_running()?;
        let config = caddy_config(
            &settings,
            &deployments.running_ports(),
            self.authentik.installed(),
        );
        let body = serde_json::to_string_pretty(&config).map_err(|e| e.to_string())?;
        let running = caddy_running();
        if running && applied.as_deref() == Some(body.as_str()) {
            return Ok(());
        }
        fs::create_dir_all(&self.caddy_dir)
            .map_err(|e| format!("create {}: {e}", self.caddy_dir.display()))?;
        write_private(&self.caddy_dir.join(CADDY_CONFIG), body.as_bytes())?;
        if running {
            reload_caddy()?;
        } else {
            start_caddy(&self.caddy_dir)?;
        }
        *applied = Some(body);
        Ok(())
    }
}

fn address(name: &str, domain: &str) -> String {
    format!("https://{name}.{domain}")
}

fn normalized_domain(domain: &str) -> Result<String, String> {
    let domain = domain
        .trim()
        .trim_start_matches("*.")
        .trim_end_matches('.')
        .to_ascii_lowercase();
    let labels: Vec<&str> = domain.split('.').collect();
    let valid = labels.len() >= 2
        && domain.len() <= 200
        && labels.iter().all(|label| {
            !label.is_empty()
                && label.len() <= 63
                && label
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b == b'-')
                && !label.starts_with('-')
                && !label.ends_with('-')
        });
    if !valid {
        return Err(format!("{domain} is not a domain, such as example.com"));
    }
    Ok(domain)
}

fn valid_name(name: &str) -> Result<String, String> {
    let name = name.trim().to_ascii_lowercase();
    let valid = !name.is_empty()
        && name.len() <= 40
        && name
            .bytes()
            .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-')
        && !name.starts_with('-')
        && !name.ends_with('-');
    if !valid {
        return Err("an address uses letters, digits and hyphens, such as my-app".into());
    }
    if RESERVED.contains(&name.as_str()) {
        return Err(format!(
            "{name} is kept for other uses. Choose another name."
        ));
    }
    Ok(name)
}

/// A password the user chose, as bcrypt keeps it.
fn hashed(password: &str) -> Result<String, String> {
    // How strong it is, is the owner's call: "Anyone" is a choice too.
    if password.is_empty() {
        return Err("choose a password, or let anyone open it".into());
    }
    bcrypt::hash(password, bcrypt::DEFAULT_COST).map_err(|e| format!("protect the password: {e}"))
}

/// The address the server reaches the internet from: its public one on a
/// VPS, which is where the wildcard record must point.
fn server_address() -> Option<IpAddr> {
    let socket = std::net::UdpSocket::bind("0.0.0.0:0").ok()?;
    socket.connect("1.1.1.1:53").ok()?;
    Some(socket.local_addr().ok()?.ip())
}

fn resolves_to(name: &str, here: IpAddr) -> bool {
    (name, 443)
        .to_socket_addrs()
        .is_ok_and(|mut addresses| addresses.any(|address| address.ip() == here))
}

/// Caddy's whole configuration. A published app with a running deployment
/// gets a route to it, behind its password when private; one without gets
/// the "not running" page. Any other name gets "No app here".
fn caddy_config(settings: &Settings, running: &BTreeMap<String, u16>, authentik: bool) -> Value {
    let domain = settings.domain.as_deref().unwrap_or_default();
    let mut routes: Vec<Value> = settings
        .apps
        .iter()
        .map(|(project_id, route)| {
            let host = format!("{}.{domain}", route.name);
            let mut handle = vec![sign_in::strip_identity()];
            if route.access == Access::Company {
                // No provider would be no gate: then no way in.
                if settings.sign_in.is_none() {
                    let page = page(503, NO_SIGN_IN);
                    return json!({"match": [{"host": [host]}], "handle": [page], "terminal": true});
                }
                handle.push(sign_in::caddy_gate(
                    domain,
                    &route.groups,
                    page(403, NOT_YOURS),
                ));
            }
            if let (Access::Private, Some(hash)) = (route.access, &route.password_hash) {
                handle.push(json!({
                    "handler": "authentication",
                    "providers": {"http_basic": {
                        "hash": {"algorithm": "bcrypt"},
                        "realm": route.name,
                        "accounts": [{
                            "username": route.name,
                            "password": base64::engine::general_purpose::STANDARD.encode(hash),
                        }],
                    }},
                }));
            }
            match running.get(project_id) {
                Some(port) => handle.push(json!({
                    "handler": "reverse_proxy",
                    "upstreams": [{"dial": format!("127.0.0.1:{port}")}],
                    // Development servers answer only their own name.
                    "headers": {"request": {"set": {"Host": ["localhost"]}}},
                })),
                None => handle.push(page(503, NOT_RUNNING)),
            }
            json!({"match": [{"host": [host]}], "handle": handle, "terminal": true})
        })
        .collect();
    if settings.sign_in.is_some() {
        routes.insert(0, sign_in::caddy_route(domain));
    }
    if authentik {
        routes.insert(
            0,
            json!({
                "match": [{"host": [format!("{}.{domain}", authentik::NAME)]}],
                "handle": [{
                    "handler": "reverse_proxy",
                    "upstreams": [{"dial": format!("127.0.0.1:{}", authentik::PORT)}],
                }],
                "terminal": true,
            }),
        );
    }
    routes.push(json!({"handle": [page(404, NO_APP)]}));
    json!({
        "admin": {"listen": CADDY_ADMIN},
        "apps": {"http": {"servers": {"public": {
            // Port 80 is Caddy's own: it redirects every app's address to
            // HTTPS there, and answers certificate challenges.
            "listen": [":443"],
            "routes": routes,
            // An app that is starting or has stopped answers with nothing.
            // Other errors, such as a password challenge, pass as they are.
            "errors": {"routes": [{
                "match": [{"expression": "{http.error.status_code} >= 502"}],
                "handle": [page(503, NOT_RUNNING)],
            }]},
        }}}},
    })
}

fn page(status: u16, text: &str) -> Value {
    json!({
        "handler": "static_response",
        "status_code": status,
        "headers": {"Content-Type": ["text/html; charset=utf-8"]},
        "body": format!(
            "<!doctype html><meta name=viewport content=\"width=device-width\">\
             <title>{text}</title><body style=\"font:16px system-ui;display:grid;\
             place-items:center;height:90vh;color:#444\"><p>{text}</p>"
        ),
    })
}

const NOT_RUNNING: &str = "This app is starting or has stopped. Try again in a moment.";
const NO_APP: &str = "No app here.";
const NOT_YOURS: &str = "You are signed in, but this app is for other groups at your company.";
const NO_SIGN_IN: &str = "This app opens with company sign-in, which is not set up.";
const NO_PROVIDER: &str = "connect your company's sign-in in Settings first";

fn caddy_running() -> bool {
    docker::docker_command()
        .args(["inspect", "-f", "{{.State.Running}}", CADDY_CONTAINER])
        .output()
        .is_ok_and(|output| output.status.success() && output.stdout.starts_with(b"true"))
}

fn start_caddy(dir: &Path) -> Result<(), String> {
    let _ = docker::docker_command()
        .args(["rm", "-f", CADDY_CONTAINER])
        .output();
    let mount = format!("{}:/etc/commitarium:ro", dir.display());
    let output = docker::docker_command()
        .args([
            "run",
            "-d",
            "--name",
            CADDY_CONTAINER,
            "--restart",
            "unless-stopped",
            "--network",
            "host",
            "--log-opt",
            "max-size=10m",
            "--log-opt",
            "max-file=3",
            "-v",
            &format!("{CADDY_DATA}:/data"),
            "-v",
            &mount,
            CADDY_IMAGE,
            "caddy",
            "run",
            "--config",
        ])
        .arg(format!("/etc/commitarium/{CADDY_CONFIG}"))
        .output()
        .map_err(|e| format!("start Caddy: {e}"))?;
    if !output.status.success() {
        return Err(format!(
            "start Caddy: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    Ok(())
}

fn reload_caddy() -> Result<(), String> {
    let output = docker::docker_command()
        .args([
            "exec",
            CADDY_CONTAINER,
            "caddy",
            "reload",
            "--address",
            CADDY_ADMIN,
            "--config",
        ])
        .arg(format!("/etc/commitarium/{CADDY_CONFIG}"))
        .output()
        .map_err(|e| format!("reload Caddy: {e}"))?;
    if !output.status.success() {
        return Err(format!(
            "reload Caddy: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    Ok(())
}

pub(crate) fn write_private(path: &Path, body: &[u8]) -> Result<(), String> {
    let temporary = path.with_extension("new");
    fs::write(&temporary, body)
        .and_then(|()| set_private(&temporary))
        .and_then(|()| fs::rename(&temporary, path))
        .map_err(|e| format!("save {}: {e}", path.display()))
}

#[cfg(unix)]
fn set_private(path: &Path) -> std::io::Result<()> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(0o600))
}

#[cfg(not(unix))]
fn set_private(_path: &Path) -> std::io::Result<()> {
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn settings() -> Settings {
        let mut settings = Settings {
            domain: Some("example.com".into()),
            apps: BTreeMap::new(),
            sign_in: None,
        };
        settings.apps.insert(
            "prj_todo".into(),
            AppRoute {
                name: "todo".into(),
                access: Access::Private,
                password_hash: Some("$2b$12$hash".into()),
                password: Some("tasks".into()),
                groups: Vec::new(),
            },
        );
        settings.apps.insert(
            "prj_blog".into(),
            AppRoute {
                name: "blog".into(),
                access: Access::Public,
                password_hash: None,
                password: None,
                groups: Vec::new(),
            },
        );
        settings
    }

    #[test]
    fn a_running_app_is_proxied_and_a_private_one_asks_first() {
        let running = BTreeMap::from([("prj_todo".to_string(), 32778)]);
        let config = caddy_config(&settings(), &running, false);
        let routes = &config["apps"]["http"]["servers"]["public"]["routes"];
        let todo = routes
            .as_array()
            .unwrap()
            .iter()
            .find(|route| route["match"][0]["host"][0] == "todo.example.com")
            .unwrap();
        // No visitor sets who signed in.
        assert_eq!(todo["handle"][0]["handler"], "headers");
        assert_eq!(todo["handle"][1]["handler"], "authentication");
        assert_eq!(todo["handle"][2]["upstreams"][0]["dial"], "127.0.0.1:32778");
        let blog = routes
            .as_array()
            .unwrap()
            .iter()
            .find(|route| route["match"][0]["host"][0] == "blog.example.com")
            .unwrap();
        // Public and not running: straight to the "not running" page.
        assert_eq!(blog["handle"][1]["handler"], "static_response");
        assert_eq!(blog["handle"][1]["status_code"], 503);
        // Anything else: no app.
        assert_eq!(
            routes.as_array().unwrap().last().unwrap()["handle"][0]["status_code"],
            404
        );
        assert_eq!(config["admin"]["listen"], CADDY_ADMIN);
        // A password challenge is no stopped app.
        let errors = &config["apps"]["http"]["servers"]["public"]["errors"]["routes"][0];
        assert_eq!(
            errors["match"][0]["expression"],
            "{http.error.status_code} >= 502"
        );
    }

    #[test]
    fn names_and_domains_are_checked() {
        assert_eq!(valid_name(" Todo-App ").unwrap(), "todo-app");
        assert!(valid_name("www").is_err());
        assert!(valid_name("-todo").is_err());
        assert!(valid_name("to do").is_err());
        assert_eq!(
            normalized_domain("*.Einarlogi.is.").unwrap(),
            "einarlogi.is"
        );
        assert!(normalized_domain("localhost").is_err());
        assert!(normalized_domain("bad_domain.is").is_err());
    }

    #[test]
    fn a_public_app_asks_for_no_password_but_keeps_it_for_later() {
        let mut settings = settings();
        settings.apps.get_mut("prj_todo").unwrap().access = Access::Public;
        let running = BTreeMap::from([("prj_todo".to_string(), 32778)]);
        let config = caddy_config(&settings, &running, false);
        let todo = config["apps"]["http"]["servers"]["public"]["routes"]
            .as_array()
            .unwrap()
            .iter()
            .find(|route| route["match"][0]["host"][0] == "todo.example.com")
            .unwrap()
            .clone();
        assert_eq!(todo["handle"][1]["handler"], "reverse_proxy");
        assert!(settings.apps["prj_todo"].password_hash.is_some());
    }

    #[test]
    fn a_chosen_password_is_kept_only_as_a_hash() {
        assert!(hashed("").is_err());
        assert!(hashed("short").is_ok());
        let hash = hashed("tasks for friends").unwrap();
        assert!(hash.starts_with("$2"));
        assert!(bcrypt::verify("tasks for friends", &hash).unwrap());
    }

    fn route<'a>(config: &'a Value, host: &str) -> &'a Value {
        config["apps"]["http"]["servers"]["public"]["routes"]
            .as_array()
            .unwrap()
            .iter()
            .find(|route| route["match"][0]["host"][0] == host)
            .unwrap()
    }

    #[test]
    fn a_company_app_is_gated_and_its_sign_in_address_served() {
        let mut settings = settings();
        let todo = settings.apps.get_mut("prj_todo").unwrap();
        todo.access = Access::Company;
        todo.groups = vec!["sales".into()];
        settings.sign_in = Some(Provider {
            issuer: "https://login.example.com".into(),
            client_id: "commitarium".into(),
            client_secret: "secret".into(),
            scopes: None,
            groups_claim: None,
            cookie_secret: "c".repeat(32),
        });
        let running = BTreeMap::from([("prj_todo".to_string(), 32778)]);
        let config = caddy_config(&settings, &running, false);
        let todo = route(&config, "todo.example.com");
        assert_eq!(todo["handle"][0]["handler"], "headers");
        assert_eq!(
            todo["handle"][1]["rewrite"]["uri"],
            "/oauth2/auth?allowed_groups=sales"
        );
        assert_eq!(todo["handle"][2]["upstreams"][0]["dial"], "127.0.0.1:32778");
        let sign_in = route(&config, "sign-in.example.com");
        assert_eq!(
            sign_in["handle"][0]["upstreams"][0]["dial"],
            "127.0.0.1:4180"
        );
        // authentik on this server gets its own address.
        let config = caddy_config(&settings, &running, true);
        let auth = route(&config, "auth.example.com");
        assert_eq!(auth["handle"][0]["upstreams"][0]["dial"], "127.0.0.1:9400");
        // Without a provider there is no way in, not an open door.
        settings.sign_in = None;
        let config = caddy_config(&settings, &running, false);
        let todo = route(&config, "todo.example.com");
        assert_eq!(todo["handle"][0]["status_code"], 503);
        assert_eq!(todo["handle"].as_array().unwrap().len(), 1);
    }

    #[test]
    fn company_access_needs_a_provider_and_disconnecting_needs_no_users() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        let mut settings = settings();
        publishing.write(&settings).unwrap();
        assert_eq!(
            publishing
                .set_access("prj_blog", Access::Company)
                .unwrap_err(),
            NO_PROVIDER
        );
        settings.sign_in = Some(Provider {
            issuer: "https://login.example.com".into(),
            client_id: "commitarium".into(),
            client_secret: "secret".into(),
            scopes: None,
            groups_claim: None,
            cookie_secret: "c".repeat(32),
        });
        publishing.write(&settings).unwrap();
        publishing.set_access("prj_blog", Access::Company).unwrap();
        publishing
            .set_groups("prj_blog", &[" sales ".into(), "sales".into(), "it".into()])
            .unwrap();
        assert_eq!(
            publishing.read().unwrap().apps["prj_blog"].groups,
            ["it", "sales"]
        );
        assert!(publishing
            .disconnect_sign_in()
            .unwrap_err()
            .contains("blog"));
        publishing.set_access("prj_blog", Access::Public).unwrap();
        publishing.disconnect_sign_in().unwrap();
        assert!(publishing.read().unwrap().sign_in.is_none());
    }

    /// Writes a company app's Caddy config and gate arguments for a live
    /// run against a test provider: `SIGN_IN_TEST_DIR=… cargo test -- --ignored`.
    #[test]
    #[ignore]
    fn write_a_sign_in_test_setup() {
        let dir = std::path::PathBuf::from(std::env::var("SIGN_IN_TEST_DIR").unwrap());
        let groups: Vec<String> = std::env::var("SIGN_IN_TEST_GROUPS")
            .map(|groups| groups.split(',').map(String::from).collect())
            .unwrap_or_default();
        // With SIGN_IN_TEST_AUTHENTIK set, authentik is set up as the
        // provider, its files in the same directory.
        let authentik = std::env::var("SIGN_IN_TEST_AUTHENTIK").is_ok();
        let provider = if authentik {
            let connection = Authentik::new(&dir).install("example.test").unwrap();
            Provider {
                issuer: connection.issuer,
                client_id: connection.client_id,
                client_secret: connection.client_secret,
                scopes: None,
                groups_claim: None,
                cookie_secret: "c".repeat(32),
            }
        } else {
            Provider {
                issuer: std::env::var("SIGN_IN_TEST_ISSUER").unwrap(),
                client_id: "commitarium".into(),
                client_secret: "secret".into(),
                scopes: None,
                groups_claim: None,
                cookie_secret: "c".repeat(32),
            }
        };
        let mut settings = Settings {
            domain: Some("example.test".into()),
            apps: BTreeMap::new(),
            sign_in: Some(provider.clone()),
        };
        settings.apps.insert(
            "prj_todo".into(),
            AppRoute {
                name: "todo".into(),
                access: Access::Company,
                password_hash: None,
                password: None,
                groups,
            },
        );
        let running = BTreeMap::from([("prj_todo".to_string(), 3000)]);
        let config = caddy_config(&settings, &running, authentik);
        fs::write(dir.join("caddy.json"), config.to_string()).unwrap();
        fs::write(dir.join("client-secret"), &provider.client_secret).unwrap();
        fs::write(
            dir.join("gate-args"),
            sign_in::arguments(&provider, "example.test").join("\n"),
        )
        .unwrap();
    }
}
