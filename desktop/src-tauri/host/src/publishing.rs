//! Publishing (PUBLISHING_PLAN.md, D2): a server's domains, and an address
//! for each deployed app, served by Caddy. Caddy is the only public door. It
//! runs in a container on the host's network, so it reaches the deployments'
//! loopback ports, and its admin API is a socket inside that container, never
//! a port. The host writes Caddy's whole configuration from the settings and
//! the running deployments, and reloads it when it changes.
//!
//! A server can hold several domains (MULTI_DOMAIN_PLAN.md). Each has its own
//! DNS mode, apps page and company sign-in; an app has one main address and
//! any number of extra ones, on any of them. The empty name is the domain
//! itself, for a website.

use crate::authentik::{self, Authentik};
use crate::deployments::Deployments;
use crate::docker;
use crate::sign_in::{self, Gate, Provider};
use crate::tailscale::{self, Tailscale};
use base64::Engine as _;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::collections::BTreeMap;
use std::fs;
use std::io::ErrorKind;
use std::net::IpAddr;
#[cfg(not(test))]
use std::net::ToSocketAddrs;
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
/// Names that read as the server's own, or as something else's, on every
/// domain. `www` is a bare-domain app's redirect. `api` stays free: it is
/// the name a deployed backend wants, and nothing of Commitarium's uses it.
const RESERVED: &[&str] = &[
    "www",
    "mail",
    "smtp",
    "imap",
    "ftp",
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

/// Where an app answers: a name under one of the server's domains. The
/// empty name is the domain itself.
#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub struct Address {
    pub domain: String,
    #[serde(default)]
    pub name: String,
}

impl Address {
    fn host(&self) -> String {
        host(&self.name, &self.domain)
    }

    fn is_bare(&self) -> bool {
        self.name.is_empty()
    }
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct AppRoute {
    /// The domain of its main address. Empty only for an app published on
    /// the private network before the server had a domain.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    domain: String,
    /// Its main address's name; empty for the domain itself.
    name: String,
    /// For an app at a bare domain: whether `www.<domain>` answers too, by
    /// sending people to the bare address. On unless the owner turns it off.
    #[serde(default = "yes", skip_serializing_if = "is_yes")]
    www: bool,
    /// Its extra addresses, which reach the same running app.
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    aliases: Vec<Address>,
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
    /// Whether the apps page lists it (APPS_PAGE_PLAN.md): yes unless the
    /// owner hides it, say while it is a work in progress.
    #[serde(default = "yes", skip_serializing_if = "is_yes")]
    listed: bool,
    /// Its port on the Tailscale name, in homelab mode: given once and kept,
    /// so its address never changes.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    port: Option<u16>,
}

impl AppRoute {
    fn main(&self) -> Address {
        Address {
            domain: self.domain.clone(),
            name: self.name.clone(),
        }
    }

    /// Every address it answers on, its main one first.
    fn addresses(&self) -> Vec<Address> {
        let mut addresses = vec![self.main()];
        addresses.extend(self.aliases.iter().cloned());
        addresses
    }

    /// The domains it has an address on, its main one first.
    fn domains(&self) -> Vec<String> {
        let mut domains: Vec<String> = Vec::new();
        for address in self.addresses() {
            if !domains.contains(&address.domain) {
                domains.push(address.domain);
            }
        }
        domains
    }

    /// Its address on a domain: the main one when it is there.
    fn address_on(&self, domain: &str) -> Option<Address> {
        self.addresses()
            .into_iter()
            .find(|address| address.domain == domain)
    }

    /// The `www` names that send people on to its bare addresses.
    fn www_redirects(&self) -> Vec<(String, String)> {
        if !self.www {
            return Vec::new();
        }
        self.addresses()
            .into_iter()
            .filter(Address::is_bare)
            .map(|address| (format!("www.{}", address.domain), address.domain))
            .collect()
    }

    /// Every name it needs DNS for, with its domain: each address, and the
    /// `www` of each bare one.
    fn dns_names(&self) -> Vec<Address> {
        let mut names = self.addresses();
        for (_, domain) in self.www_redirects() {
            names.push(Address {
                domain,
                name: "www".into(),
            });
        }
        names
    }
}

/// Who reaches the apps (HOMELAB_PLAN.md): anyone on the internet, at the
/// server's domains, or only the owner's devices, on their Tailscale network.
#[derive(Clone, Copy, Debug, Default, Deserialize, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Reach {
    #[default]
    Internet,
    Private,
}

fn is_internet(reach: &Reach) -> bool {
    *reach == Reach::Internet
}

/// How a domain's DNS points at the server: one wildcard record for every
/// name, or a record for each name in use, for a company that keeps no
/// wildcard.
#[derive(Clone, Copy, Debug, Default, Deserialize, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Records {
    #[default]
    Wildcard,
    EachName,
}

fn is_wildcard(records: &Records) -> bool {
    *records == Records::Wildcard
}

/// Where the owner said the server is, asked once when they first open it,
/// so the app recommends how to serve the apps.
#[derive(Clone, Copy, Debug, Deserialize, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Placement {
    /// A computer on their own network: homelab mode.
    Home,
    /// A server in the cloud: their own domain.
    Cloud,
    Other,
}

/// Where apps are served: at their domains, or on the Tailscale name.
#[derive(Clone, Copy)]
enum Base<'a> {
    Internet,
    Tailnet { name: &'a str },
}

impl Base<'_> {
    fn app_url(&self, route: &AppRoute) -> String {
        match self {
            Base::Internet if route.domain.is_empty() => String::new(),
            Base::Internet => address(&route.name, &route.domain),
            Base::Tailnet { name, .. } => match route.port {
                Some(port) => format!("https://{name}:{port}"),
                None => String::new(),
            },
        }
    }

    /// Every address of an app, its main one first.
    fn app_urls(&self, route: &AppRoute) -> Vec<String> {
        match self {
            Base::Internet if route.domain.is_empty() => Vec::new(),
            Base::Internet => route
                .addresses()
                .iter()
                .map(|address| format!("https://{}", address.host()))
                .collect(),
            Base::Tailnet { .. } => vec![self.app_url(route)],
        }
    }
}

/// The first port an app gets on the Tailscale name.
const FIRST_APP_PORT: u16 = 8001;

/// Gives every app without a port the next free one.
fn assign_ports(settings: &mut Settings) {
    let mut used: Vec<u16> = settings
        .apps
        .values()
        .filter_map(|route| route.port)
        .collect();
    for route in settings.apps.values_mut() {
        if route.port.is_none() {
            let port = (FIRST_APP_PORT..)
                .find(|port| !used.contains(port))
                .unwrap_or(FIRST_APP_PORT);
            used.push(port);
            route.port = Some(port);
        }
    }
}

fn default_apps_page_name() -> String {
    APPS_PAGE_NAME.to_string()
}

fn yes() -> bool {
    true
}

fn is_yes(value: &bool) -> bool {
    *value
}

/// The apps page at `<name>.<domain>`, `apps` unless the owner chose
/// another name: who may open it, as for an app.
#[derive(Clone, Debug, Deserialize, Serialize)]
struct AppsPage {
    #[serde(default = "default_apps_page_name")]
    name: String,
    access: Access,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    password_hash: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    password: Option<String>,
}

/// One of the server's domains, with what is its own: how its DNS points
/// here, its company sign-in and its apps page.
#[derive(Clone, Debug, Deserialize, Serialize)]
struct Domain {
    name: String,
    #[serde(default, skip_serializing_if = "is_wildcard")]
    records: Records,
    /// The company's sign-in provider for this domain, once connected.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    sign_in: Option<Provider>,
    /// Which gate serves its sign-in, given when it is connected.
    #[serde(default, skip_serializing_if = "Gate::is_first")]
    gate: Gate,
    /// Its apps page, once turned on.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    apps_page: Option<AppsPage>,
}

impl Domain {
    fn new(name: String, records: Records) -> Self {
        Self {
            name,
            records,
            sign_in: None,
            gate: Gate::default(),
            apps_page: None,
        }
    }

    /// Its gate, when it has sign-in: where Caddy asks about a request.
    fn gate(&self) -> Option<(&str, Gate)> {
        self.sign_in
            .as_ref()
            .map(|_| (self.name.as_str(), self.gate))
    }

    fn page_host(&self) -> Option<String> {
        self.apps_page
            .as_ref()
            .map(|page| host(&page.name, &self.name))
    }
}

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
#[serde(from = "Saved")]
struct Settings {
    /// The first is the one older apps and clients know as "the domain".
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    domains: Vec<Domain>,
    #[serde(default)]
    apps: BTreeMap<String, AppRoute>,
    /// The apps page on the private network, which has no domain: at the
    /// Tailscale name itself.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    private_apps_page: Option<AppsPage>,
    #[serde(default, skip_serializing_if = "is_internet")]
    reach: Reach,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    placement: Option<Placement>,
}

/// The file as this version saves it, or as one before several domains did,
/// with its one domain and that domain's settings at the top. Those move
/// into the first domain on reading; only the new shape is written.
#[derive(Deserialize)]
struct Saved {
    #[serde(default)]
    domains: Vec<Domain>,
    #[serde(default)]
    apps: BTreeMap<String, AppRoute>,
    #[serde(default)]
    private_apps_page: Option<AppsPage>,
    #[serde(default)]
    reach: Reach,
    #[serde(default)]
    placement: Option<Placement>,
    #[serde(default)]
    domain: Option<String>,
    #[serde(default)]
    records: Records,
    #[serde(default)]
    sign_in: Option<Provider>,
    #[serde(default)]
    apps_page: Option<AppsPage>,
}

impl From<Saved> for Settings {
    fn from(saved: Saved) -> Self {
        let mut settings = Settings {
            domains: saved.domains,
            apps: saved.apps,
            private_apps_page: saved.private_apps_page,
            reach: saved.reach,
            placement: saved.placement,
        };
        let had_domain = saved.domain.is_some();
        if let Some(name) = saved.domain.filter(|_| settings.domains.is_empty()) {
            settings.domains.push(Domain {
                sign_in: saved.sign_in,
                apps_page: saved.apps_page.clone(),
                ..Domain::new(name, saved.records)
            });
        }
        // The one apps page served the private network too.
        if saved.reach == Reach::Private || !had_domain {
            settings.private_apps_page = settings.private_apps_page.or(saved.apps_page);
        }
        settings.adopt_apps();
        settings
    }
}

impl Settings {
    /// Where apps are served now, if anywhere yet.
    fn base<'a>(&'a self, tailnet: Option<&'a tailscale::Status>) -> Option<Base<'a>> {
        match self.reach {
            Reach::Internet => (!self.domains.is_empty()).then_some(Base::Internet),
            Reach::Private => {
                let (name, _) = tailnet?.serving()?;
                Some(Base::Tailnet { name })
            }
        }
    }

    /// Apps without a domain go to the first one, as they did when a server
    /// had only one.
    fn adopt_apps(&mut self) {
        let Some(first) = self.domains.first().map(|domain| domain.name.clone()) else {
            return;
        };
        for route in self.apps.values_mut() {
            if route.domain.is_empty() {
                route.domain = first.clone();
            }
        }
    }

    /// A domain by name, or the first when none is named.
    fn domain(&self, name: Option<&str>) -> Result<&Domain, String> {
        let index = self.domain_index(name)?;
        Ok(&self.domains[index])
    }

    fn domain_mut(&mut self, name: Option<&str>) -> Result<&mut Domain, String> {
        let index = self.domain_index(name)?;
        Ok(&mut self.domains[index])
    }

    fn domain_index(&self, name: Option<&str>) -> Result<usize, String> {
        match name.map(|name| name.trim().trim_end_matches('.').to_ascii_lowercase()) {
            None if self.domains.is_empty() => Err(NO_DOMAIN.into()),
            None => Ok(0),
            Some(name) => self
                .domains
                .iter()
                .position(|domain| domain.name == name)
                .ok_or_else(|| format!("{name} is not one of this server's domains")),
        }
    }

    /// Whether an address is free for this app: no other app, no apps page
    /// has it. Names are per domain: notes on one is free on another.
    fn is_free(&self, address: &Address, project_id: &str) -> Result<(), String> {
        let host = address.host();
        if self
            .apps
            .iter()
            .filter(|(other, _)| *other != project_id)
            .any(|(_, route)| route.addresses().contains(address))
        {
            return Err(format!("{host} is already another app's address"));
        }
        let page = self
            .domains
            .iter()
            .any(|domain| domain.page_host().as_deref() == Some(host.as_str()));
        if page {
            return Err(format!(
                "{host} is the apps page's address. Choose another name."
            ));
        }
        Ok(())
    }

    /// The domain whose apps page has this host, as the request names it.
    fn page_domain(&self, host: &str) -> Option<&Domain> {
        let host = host.split(':').next().unwrap_or(host).to_ascii_lowercase();
        self.domains
            .iter()
            .find(|domain| domain.page_host().as_deref() == Some(host.as_str()))
    }

    /// Company sign-in is per domain: an app behind it needs it on every
    /// domain it has an address on.
    fn sign_in_for(&self, route: &AppRoute) -> Result<(), String> {
        for name in route.domains() {
            let connected = self
                .domains
                .iter()
                .any(|domain| domain.name == name && domain.sign_in.is_some());
            if !connected {
                return Err(self.no_provider(&name));
            }
        }
        Ok(())
    }

    fn no_provider(&self, domain: &str) -> String {
        match self.domains.len() {
            0 | 1 => NO_PROVIDER.into(),
            _ => format!("connect your company's sign-in for {domain} in Settings first"),
        }
    }

    /// The gates the domains with sign-in run.
    fn gates(&self) -> Vec<(Gate, &Provider, &str)> {
        self.domains
            .iter()
            .filter_map(|domain| {
                Some((domain.gate, domain.sign_in.as_ref()?, domain.name.as_str()))
            })
            .collect()
    }

    /// The domain authentik lives at, when it is set up. A server set up
    /// before several domains has it at the domain whose sign-in is its
    /// first application, or else at the first domain.
    fn authentik_home(&self, authentik: &Authentik) -> Option<String> {
        if !authentik.installed() {
            return None;
        }
        authentik
            .home()
            .or_else(|| {
                self.domains
                    .iter()
                    .find(|domain| {
                        domain.sign_in.as_ref().is_some_and(|provider| {
                            provider.issuer == authentik::issuer(&domain.name, &domain.name)
                        })
                    })
                    .map(|domain| domain.name.clone())
            })
            .or_else(|| self.domains.first().map(|domain| domain.name.clone()))
    }

    /// The domains whose sign-in is this server's authentik.
    fn authentik_domains(&self, home: &str) -> Vec<String> {
        self.domains
            .iter()
            .filter(|domain| {
                domain
                    .sign_in
                    .as_ref()
                    .is_some_and(|provider| authentik::serves(home, &provider.issuer))
            })
            .map(|domain| domain.name.clone())
            .collect()
    }

    /// The apps page an app is shown in, by origin: each of its domains'.
    fn page_origins(&self, route: &AppRoute) -> Vec<String> {
        route
            .domains()
            .iter()
            .filter_map(|name| {
                let domain = self.domains.iter().find(|domain| domain.name == *name)?;
                Some(format!("https://{}", domain.page_host()?))
            })
            .collect()
    }

    /// What is wrong with an app's DNS records, if anything: each address
    /// with a record for each name, and a bare address and its `www` with a
    /// wildcard, which does not cover the domain itself.
    fn record_problem(&self, route: &AppRoute) -> Option<String> {
        let problems: Vec<String> = route
            .dns_names()
            .iter()
            .filter(|address| {
                // `www` is never an app's name: here it is a redirect's.
                address.is_bare()
                    || address.name == "www"
                    || self.domains.iter().any(|domain| {
                        domain.name == address.domain && domain.records == Records::EachName
                    })
            })
            .filter_map(|address| points_here(&address.name, &address.domain).err())
            .collect();
        (!problems.is_empty()).then(|| problems.join(" "))
    }
}

/// An address as a client sees it.
#[derive(Serialize)]
pub struct AddressShown {
    domain: String,
    name: String,
    url: String,
}

/// A published app as a client sees it.
#[derive(Serialize)]
pub struct PublishedApp {
    project_id: String,
    /// Its main address: its domain and name; the empty name is the domain.
    domain: String,
    name: String,
    access: Access,
    /// Its main address.
    url: String,
    /// Every address, its main one first.
    urls: Vec<String>,
    /// For a bare-domain app: whether `www.` sends people to it.
    www: bool,
    /// Its extra addresses.
    aliases: Vec<AddressShown>,
    /// Whether its deployment runs, so the address answers with the app.
    live: bool,
    groups: Vec<String>,
    /// Whether the apps page lists it.
    listed: bool,
    /// What is wrong with its records, when an address does not point here
    /// (such as after the domain changed, or a bare domain with no record).
    record_problem: Option<String>,
}

/// The apps page as a client sees it.
#[derive(Serialize)]
pub struct AppsPageShown {
    name: String,
    access: Access,
    url: String,
}

/// A domain as a client sees it.
#[derive(Serialize)]
pub struct DomainShown {
    name: String,
    records: Records,
    /// Its company sign-in, when one is connected.
    sign_in: Option<sign_in::Connected>,
    /// Whether that sign-in is this server's authentik.
    authentik: bool,
    /// Its apps page, when it is on.
    apps_page: Option<AppsPageShown>,
}

/// An app as the apps page shows it to the person looking.
#[derive(Debug, PartialEq, Serialize)]
pub struct CatalogApp {
    /// Its name at the page's domain; empty for the domain itself.
    name: String,
    url: String,
    access: Access,
    live: bool,
}

#[derive(Serialize)]
pub struct Overview {
    domains: Vec<DomainShown>,
    /// The first domain, and below its sign-in, apps page and records: what
    /// clients before several domains read. Kept for one release.
    domain: Option<String>,
    /// The address the domains' records should point at.
    server_address: Option<String>,
    apps: Vec<PublishedApp>,
    /// The first domain's sign-in provider, when one is connected.
    sign_in: Option<sign_in::Connected>,
    /// authentik on this server, and what it needs.
    authentik: authentik::Status,
    /// The first domain's apps page when it is on; on the private network,
    /// the one there.
    apps_page: Option<AppsPageShown>,
    reach: Reach,
    /// Tailscale, in homelab mode.
    tailscale: Option<tailscale::Status>,
    records: Records,
    /// Where the owner said the server is, once asked.
    placement: Option<Placement>,
}

/// What publishing an app answers: its address.
#[derive(Serialize)]
pub struct Published {
    url: String,
    /// Every address, its main one first.
    urls: Vec<String>,
}

/// Where an app is published, as a client asks: each part kept as it was,
/// or the first domain and `www` on for a new app, when left out.
#[derive(Default, Deserialize)]
pub struct Placing {
    #[serde(default)]
    pub domain: Option<String>,
    #[serde(default)]
    pub www: Option<bool>,
    #[serde(default)]
    pub aliases: Option<Vec<Address>>,
}

#[derive(Clone)]
pub struct Publishing {
    file: PathBuf,
    caddy_dir: PathBuf,
    authentik: Authentik,
    tailscale: Tailscale,
    /// Orders changes to the file, and holds the configuration last loaded.
    applied: Arc<Mutex<Option<String>>>,
}

impl Publishing {
    pub fn new(data_dir: &Path) -> Self {
        Self {
            file: data_dir.join(FILE),
            caddy_dir: data_dir.join(CADDY_DIR),
            authentik: Authentik::new(data_dir),
            tailscale: Tailscale::new(data_dir),
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

    fn lock(&self) -> Result<std::sync::MutexGuard<'_, Option<String>>, String> {
        self.applied
            .lock()
            .map_err(|_| "publishing is unavailable".to_string())
    }

    pub fn overview(&self, deployments: &Deployments) -> Result<Overview, String> {
        let settings = self.read()?;
        let running = deployments.running_ports();
        let private = settings.reach == Reach::Private;
        let tailnet = private.then(|| self.tailscale.status());
        let base = settings.base(tailnet.as_ref());
        let home = settings.authentik_home(&self.authentik);
        let domains: Vec<DomainShown> = settings
            .domains
            .iter()
            .map(|domain| DomainShown {
                name: domain.name.clone(),
                records: domain.records,
                sign_in: domain
                    .sign_in
                    .as_ref()
                    .map(|provider| provider.shown(Some(&domain.name))),
                authentik: domain.sign_in.as_ref().is_some_and(|provider| {
                    home.as_deref()
                        .is_some_and(|home| authentik::serves(home, &provider.issuer))
                }),
                apps_page: domain.apps_page.as_ref().map(|page| AppsPageShown {
                    name: page.name.clone(),
                    access: page.access,
                    url: address(&page.name, &domain.name),
                }),
            })
            .collect();
        let first = settings.domains.first();
        let apps_page = match (private, base) {
            (true, Some(Base::Tailnet { name })) => {
                settings
                    .private_apps_page
                    .as_ref()
                    .map(|page| AppsPageShown {
                        name: page.name.clone(),
                        access: page.access,
                        url: format!("https://{name}"),
                    })
            }
            (true, _) => None,
            (false, _) => first.and_then(|domain| {
                let page = domain.apps_page.as_ref()?;
                Some(AppsPageShown {
                    name: page.name.clone(),
                    access: page.access,
                    url: address(&page.name, &domain.name),
                })
            }),
        };
        Ok(Overview {
            server_address: server_address().map(|ip| ip.to_string()),
            apps: settings
                .apps
                .iter()
                .map(|(project_id, route)| PublishedApp {
                    project_id: project_id.clone(),
                    domain: route.domain.clone(),
                    name: route.name.clone(),
                    access: route.access,
                    url: base.map(|base| base.app_url(route)).unwrap_or_default(),
                    urls: base.map(|base| base.app_urls(route)).unwrap_or_default(),
                    www: route.www,
                    aliases: route
                        .aliases
                        .iter()
                        .map(|alias| AddressShown {
                            domain: alias.domain.clone(),
                            name: alias.name.clone(),
                            url: format!("https://{}", alias.host()),
                        })
                        .collect(),
                    live: running.contains_key(project_id),
                    groups: route.groups.clone(),
                    listed: route.listed,
                    record_problem: (!private).then(|| settings.record_problem(route)).flatten(),
                })
                .collect(),
            apps_page,
            reach: settings.reach,
            records: first.map(|domain| domain.records).unwrap_or_default(),
            placement: settings.placement,
            tailscale: tailnet.clone(),
            sign_in: first.and_then(|domain| {
                let provider = domain.sign_in.as_ref()?;
                Some(provider.shown(Some(&domain.name)))
            }),
            authentik: self.authentik.status(home.as_deref()),
            domain: first.map(|domain| domain.name.clone()),
            domains,
        })
    }

    /// Sets the first domain, as the server had one before. With a wildcard,
    /// once it points here; with a record for each name, each name is
    /// checked as it is used. Changing it moves what was at the old name.
    pub fn set_domain(&self, domain: &str, records: Records) -> Result<(), String> {
        let domain = normalized_domain(domain)?;
        wildcard_points_here(&domain, records)?;
        let _held = self.lock()?;
        let mut settings = self.read()?;
        let Some(first) = settings.domains.first().map(|first| first.name.clone()) else {
            settings.domains.push(Domain::new(domain, records));
            settings.adopt_apps();
            return self.write(&settings);
        };
        if first != domain && settings.domains.iter().any(|other| other.name == domain) {
            return Err(format!("{domain} is already one of this server's domains"));
        }
        settings.domains[0].name = domain.clone();
        settings.domains[0].records = records;
        for route in settings.apps.values_mut() {
            if route.domain == first {
                route.domain = domain.clone();
            }
            for alias in &mut route.aliases {
                if alias.domain == first {
                    alias.domain = domain.clone();
                }
            }
        }
        self.write(&settings)
    }

    /// Adds a domain to the server, checked as the first one is.
    pub fn add_domain(&self, domain: &str, records: Records) -> Result<(), String> {
        let domain = normalized_domain(domain)?;
        if self
            .read()?
            .domains
            .iter()
            .any(|other| other.name == domain)
        {
            return Err(format!("{domain} is already one of this server's domains"));
        }
        wildcard_points_here(&domain, records)?;
        let _held = self.lock()?;
        let mut settings = self.read()?;
        if settings.domains.iter().any(|other| other.name == domain) {
            return Err(format!("{domain} is already one of this server's domains"));
        }
        settings.domains.push(Domain::new(domain, records));
        settings.adopt_apps();
        self.write(&settings)
    }

    /// Lets go of a domain, once nothing is served on it any more; with no
    /// domain named, the server's only one.
    pub fn remove_domain(&self, domain: Option<&str>) -> Result<(), String> {
        let _held = self.lock()?;
        let mut settings = self.read()?;
        if settings.domains.is_empty() {
            return Ok(());
        }
        if domain.is_none() && settings.domains.len() > 1 {
            return Err("say which of this server's domains to remove".into());
        }
        let index = settings.domain_index(domain)?;
        let name = settings.domains[index].name.clone();
        if settings.reach == Reach::Internet {
            let entry = &settings.domains[index];
            let mut used = Vec::new();
            if settings
                .apps
                .values()
                .any(|route| route.domains().contains(&name))
            {
                used.push("its apps (give each a new address or take it down first)".to_string());
            }
            if entry.apps_page.is_some() {
                used.push("the apps page (turn it off first)".into());
            }
            if entry.sign_in.is_some() {
                used.push("company sign-in (disconnect it first)".into());
            }
            if settings.authentik_home(&self.authentik).as_deref() == Some(name.as_str()) {
                used.push(format!("authentik, which lives at auth.{name}"));
            }
            if !used.is_empty() {
                return Err(format!(
                    "{name} is still in use by {}.",
                    used.join(", and ")
                ));
            }
        }
        // On the private network the domains are set aside: an app there
        // keeps its name, and takes the next first domain when there is one.
        for route in settings.apps.values_mut() {
            route.aliases.retain(|alias| alias.domain != name);
            if route.domain == name {
                route.domain = String::new();
            }
        }
        settings.domains.remove(index);
        settings.adopt_apps();
        self.write(&settings)
    }

    /// Keeps where the owner said the server is.
    pub fn set_placement(&self, placement: Placement) -> Result<(), String> {
        let _held = self.lock()?;
        let mut settings = self.read()?;
        settings.placement = Some(placement);
        self.write(&settings)
    }

    /// Gives a deployed app its addresses. A private app needs a password
    /// the first time; after that it keeps its own unless a new one is given.
    pub fn publish(
        &self,
        project_id: &str,
        name: &str,
        access: Access,
        password: Option<&str>,
        placing: Placing,
    ) -> Result<Published, String> {
        let name = valid_name(name)?;
        let _held = self.lock()?;
        let mut settings = self.read()?;
        let private = settings.reach == Reach::Private;
        let previous = settings.apps.get(project_id).cloned();
        if private && access == Access::Company {
            return Err(PRIVATE_NO_COMPANY.into());
        }
        if private && name.is_empty() {
            return Err("on the private network, an app needs a name".into());
        }
        let domain = match placing.domain.as_deref() {
            Some(asked) => settings.domain(Some(asked))?.name.clone(),
            None => previous
                .as_ref()
                .map(|route| route.domain.clone())
                .filter(|domain| !domain.is_empty())
                .or_else(|| settings.domains.first().map(|first| first.name.clone()))
                .unwrap_or_default(),
        };
        if domain.is_empty() && !private {
            return Err(NO_DOMAIN.into());
        }
        let aliases = match placing.aliases {
            Some(asked) if private && !asked.is_empty() => {
                return Err("extra addresses need your own domain, on the internet".into())
            }
            Some(asked) => asked
                .iter()
                .map(|alias| {
                    Ok(Address {
                        domain: settings.domain(Some(&alias.domain))?.name.clone(),
                        name: valid_name(&alias.name)?,
                    })
                })
                .collect::<Result<Vec<_>, String>>()?,
            None => previous
                .as_ref()
                .map(|route| route.aliases.clone())
                .unwrap_or_default(),
        };
        let previous_hash = previous
            .as_ref()
            .and_then(|route| Some((route.password_hash.clone()?, route.password.clone())));
        let (groups, listed, port) = previous.as_ref().map_or((Vec::new(), true, None), |route| {
            (route.groups.clone(), route.listed, route.port)
        });
        let mut route = AppRoute {
            domain,
            name,
            www: placing
                .www
                .or(previous.as_ref().map(|route| route.www))
                .unwrap_or(true),
            aliases,
            access,
            password_hash: None,
            password: None,
            groups,
            listed,
            port,
        };
        let addresses = route.addresses();
        for (index, address) in addresses.iter().enumerate() {
            if addresses[..index].contains(address) {
                return Err(format!("{} is given twice", address.host()));
            }
            settings.is_free(address, project_id)?;
        }
        // On the private network, names are only labels: no DNS to check.
        if !private {
            for address in route.dns_names() {
                points_here(&address.name, &address.domain)?;
            }
        }
        // A public app keeps the password it had, so making it private again
        // needs no new one; Caddy asks for it only while the app is private.
        (route.password_hash, route.password) = match (access, password, previous_hash) {
            (_, Some(password), _) => (Some(hashed(password)?), Some(password.to_string())),
            (_, None, Some((hash, kept))) => (Some(hash), kept),
            (Access::Public | Access::Company, None, None) => (None, None),
            (Access::Private, None, None) => {
                return Err("choose a password for who may open it".into())
            }
        };
        if access == Access::Company {
            settings.sign_in_for(&route)?;
        }
        settings.apps.insert(project_id.to_string(), route);
        if private {
            assign_ports(&mut settings);
        }
        self.write(&settings)?;
        let tailnet = private.then(|| self.tailscale.status());
        let (url, urls) = settings
            .base(tailnet.as_ref())
            .zip(settings.apps.get(project_id))
            .map(|(base, route)| (base.app_url(route), base.app_urls(route)))
            .unwrap_or_default();
        Ok(Published { url, urls })
    }

    /// Changes a private app's password.
    pub fn set_app_password(&self, project_id: &str, password: &str) -> Result<(), String> {
        let hash = hashed(password)?;
        let _held = self.lock()?;
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
        let _held = self.lock()?;
        let mut settings = self.read()?;
        let route = settings
            .apps
            .get(project_id)
            .ok_or("this app is not published")?;
        if access == Access::Private && route.password_hash.is_none() {
            return Err("choose a password for who may open it".into());
        }
        if access == Access::Company {
            settings.sign_in_for(route)?;
        }
        if let Some(route) = settings.apps.get_mut(project_id) {
            route.access = access;
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
        let _held = self.lock()?;
        let mut settings = self.read()?;
        let route = settings
            .apps
            .get_mut(project_id)
            .ok_or("this app is not published")?;
        route.groups = groups;
        self.write(&settings)
    }

    /// Connects a domain's company sign-in provider, once it answers as
    /// one. A changed provider keeps the domain's cookie secret, so no one
    /// is signed out for nothing.
    pub fn connect_sign_in(&self, asked: sign_in::Connect) -> Result<sign_in::Connected, String> {
        let settings = self.read()?;
        let domain = settings.domain(asked.domain.as_deref())?.clone();
        sign_in_names_point_here(&domain, &[sign_in::NAME])?;
        let kept = domain.sign_in.map(|provider| provider.cookie_secret);
        // Checked before the lock: it waits on the provider.
        let provider = Provider::connect(asked, kept)?;
        let _held = self.lock()?;
        let mut settings = self.read()?;
        let shown = provider.shown(Some(&domain.name));
        connect(&mut settings, &domain.name, provider)?;
        self.write(&settings)?;
        Ok(shown)
    }

    /// Sets authentik up on this server and connects it as a domain's
    /// provider. It takes minutes: images, then its first start. One
    /// authentik serves every domain, at the address it was first set up on;
    /// each domain gets its own application in it.
    pub fn install_authentik(&self, domain: Option<&str>) -> Result<sign_in::Connected, String> {
        let settings = self.read()?;
        let entry = settings.domain(domain)?.clone();
        let home = settings
            .authentik_home(&self.authentik)
            .unwrap_or_else(|| entry.name.clone());
        let before = settings.authentik_domains(&home);
        if entry.sign_in.is_some() && !before.contains(&entry.name) {
            return Err(format!(
                "another provider is connected for {}: disconnect it first",
                entry.name
            ));
        }
        sign_in_names_point_here(&entry, &[sign_in::NAME])?;
        if let Ok(home_entry) = settings.domain(Some(&home)) {
            sign_in_names_point_here(home_entry, &[authentik::NAME])?;
        }
        let connection = self.authentik.install(&home, &before, &entry.name)?;
        let _held = self.lock()?;
        let mut settings = self.read()?;
        // Its own: no need to ask it whether it answers, before Caddy has
        // its address.
        let cookie_secret = match &settings.domain(Some(&entry.name))?.sign_in {
            Some(provider) => provider.cookie_secret.clone(),
            None => sign_in::new_cookie_secret()?,
        };
        let provider = Provider {
            end_session: Some(authentik::end_session(&connection.issuer)),
            issuer: connection.issuer,
            client_id: connection.client_id,
            client_secret: connection.client_secret,
            scopes: Some(authentik::SCOPES.into()),
            groups_claim: None,
            cookie_secret,
        };
        let shown = provider.shown(Some(&entry.name));
        connect(&mut settings, &entry.name, provider)?;
        self.write(&settings)?;
        Ok(shown)
    }

    /// authentik's admin password, for its admin page.
    pub fn authentik_password(&self) -> Result<String, String> {
        self.authentik.admin_password()
    }

    /// Disconnects a domain's provider, once no app there uses it. authentik
    /// on this server stops once no domain uses it; its data stays for
    /// setting it up again.
    pub fn disconnect_sign_in(&self, domain: Option<&str>) -> Result<(), String> {
        let _held = self.lock()?;
        let mut settings = self.read()?;
        let name = settings.domain(domain)?.name.clone();
        let home = settings.authentik_home(&self.authentik);
        let using: Vec<String> = settings
            .apps
            .values()
            .filter(|route| route.access == Access::Company && route.domains().contains(&name))
            .map(|route| route.main().host())
            .collect();
        if !using.is_empty() {
            return Err(format!(
                "{} still use company sign-in: choose who can open them first",
                using.join(", ")
            ));
        }
        let entry = settings.domain_mut(Some(&name))?;
        if entry
            .apps_page
            .as_ref()
            .is_some_and(|page| page.access == Access::Company)
        {
            return Err("the apps page uses company sign-in: choose who can open it first".into());
        }
        entry.sign_in = None;
        self.write(&settings)?;
        if let Some(home) = home {
            if settings.authentik_domains(&home).is_empty() {
                self.authentik.remove()?;
            }
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

    /// Turns a domain's apps page on with who may open it, or off with
    /// `None`. A password page needs a password the first time, as a private
    /// app does. On the private network there is one page, at the Tailscale
    /// name, whatever the domain.
    pub fn set_apps_page(
        &self,
        domain: Option<&str>,
        access: Option<Access>,
        password: Option<&str>,
        name: Option<&str>,
    ) -> Result<(), String> {
        let hash = password.map(hashed).transpose()?;
        let asked_name = name.map(valid_page_name).transpose()?;
        let _held = self.lock()?;
        let mut settings = self.read()?;
        let private = settings.reach == Reach::Private;
        let Some(access) = access else {
            if private {
                settings.private_apps_page = None;
            } else if domain.is_some() || !settings.domains.is_empty() {
                settings.domain_mut(domain)?.apps_page = None;
            }
            return self.write(&settings);
        };
        if access == Access::Company && private {
            return Err(PRIVATE_NO_COMPANY.into());
        }
        let previous = match private {
            true => settings.private_apps_page.clone(),
            false => settings.domain(domain)?.apps_page.clone(),
        };
        if !private {
            let entry = settings.domain(domain)?;
            if access == Access::Company && entry.sign_in.is_none() {
                return Err(settings.no_provider(&entry.name));
            }
        }
        let previous_name = previous.as_ref().map(|page| page.name.clone());
        let name = asked_name
            .or_else(|| previous_name.clone())
            .unwrap_or_else(default_apps_page_name);
        // On the private network the page has the Tailscale name to itself.
        if previous_name.as_deref() != Some(name.as_str()) && !private {
            let entry = settings.domain(domain)?;
            name_is_free(&settings, &name, &entry.name)?;
        }
        let (password_hash, password) = match (hash, previous) {
            (Some(hash), _) => (Some(hash), password.map(str::to_string)),
            (None, Some(page)) if page.password_hash.is_some() => {
                (page.password_hash, page.password)
            }
            (None, _) if access == Access::Private => {
                return Err("choose a password for who may open it".into())
            }
            (None, _) => (None, None),
        };
        let page = AppsPage {
            name,
            access,
            password_hash,
            password,
        };
        match private {
            true => settings.private_apps_page = Some(page),
            false => settings.domain_mut(domain)?.apps_page = Some(page),
        }
        self.write(&settings)
    }

    /// Whether a domain's apps page could live at this name: asked before
    /// offering it, so a name the company already uses is found first.
    pub fn check_apps_page_name(&self, domain: Option<&str>, name: &str) -> Result<(), String> {
        let name = valid_page_name(name)?;
        let settings = self.read()?;
        if settings.reach == Reach::Private {
            return Ok(());
        }
        let entry = settings.domain(domain)?;
        if entry
            .apps_page
            .as_ref()
            .is_some_and(|page| page.name == name)
        {
            return Ok(());
        }
        name_is_free(&settings, &name, &entry.name)
    }

    /// A domain's apps page password, for its owner to share again.
    pub fn apps_page_password(&self, domain: Option<&str>) -> Result<Option<String>, String> {
        let settings = self.read()?;
        let page = match settings.reach {
            Reach::Private => settings.private_apps_page,
            Reach::Internet => settings.domain(domain)?.apps_page.clone(),
        };
        Ok(page.ok_or("the apps page is off")?.password)
    }

    /// Lists an app on the apps pages, or hides it there.
    pub fn set_listed(&self, project_id: &str, listed: bool) -> Result<(), String> {
        let _held = self.lock()?;
        let mut settings = self.read()?;
        let route = settings
            .apps
            .get_mut(project_id)
            .ok_or("this app is not published")?;
        route.listed = listed;
        self.write(&settings)
    }

    /// Where a person signs out of every app at the page's domain, when its
    /// apps page is behind company sign-in: the page offers it. `host` is
    /// the page's address, as the request names it.
    pub fn apps_page_sign_out(&self, host: Option<&str>) -> Result<Option<String>, String> {
        let settings = self.read()?;
        if settings.reach == Reach::Private {
            return Ok(None);
        }
        let Some(domain) = host.and_then(|host| settings.page_domain(host)) else {
            return Ok(None);
        };
        let company = domain
            .apps_page
            .as_ref()
            .is_some_and(|page| page.access == Access::Company);
        Ok((company && domain.sign_in.is_some()).then(|| sign_in::sign_out_url(&domain.name)))
    }

    /// The apps the apps page at `host` shows to someone with these groups,
    /// when the page is behind company sign-in. `None` when it is off.
    pub fn catalog(
        &self,
        deployments: &Deployments,
        groups: Option<&str>,
        host: Option<&str>,
    ) -> Result<Option<Vec<CatalogApp>>, String> {
        let settings = self.read()?;
        let tailnet = (settings.reach == Reach::Private).then(|| self.tailscale.status());
        let Some(base) = settings.base(tailnet.as_ref()) else {
            return Ok(None);
        };
        Ok(catalog(
            &settings,
            &deployments.running_ports(),
            groups,
            base,
            host,
        ))
    }

    /// Chooses who reaches the apps. Private needs company sign-in gone
    /// first, since it needs a domain on the internet; it gives every app its
    /// port and starts Tailscale. Back to the internet stops Commitarium's
    /// Tailscale, keeping its state for coming back.
    pub fn set_reach(&self, reach: Reach) -> Result<(), String> {
        let _held = self.lock()?;
        let mut settings = self.read()?;
        if reach == Reach::Private && !settings.gates().is_empty() {
            return Err(
                "disconnect company sign-in first: it needs your own domain, on the internet"
                    .into(),
            );
        }
        settings.reach = reach;
        if reach == Reach::Private {
            assign_ports(&mut settings);
        }
        self.write(&settings)?;
        match reach {
            Reach::Private => self.tailscale.keep_running(),
            Reach::Internet => {
                self.tailscale.stop();
                Ok(())
            }
        }
    }

    pub fn unpublish(&self, project_id: &str) -> Result<(), String> {
        let _held = self.lock()?;
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
        let mut applied = self.lock()?;
        let settings = self.read()?;
        let (config, socket_dir) = match settings.reach {
            Reach::Internet => {
                if settings.domains.is_empty() {
                    return Ok(());
                }
                sign_in::sync(&settings.gates())?;
                self.authentik.keep_running()?;
                let home = settings.authentik_home(&self.authentik);
                let config = caddy_config(&settings, &deployments.running_ports(), home.as_deref());
                (config, None)
            }
            Reach::Private => {
                self.tailscale.keep_running()?;
                let status = self.tailscale.status();
                // Until it is signed in with certificates on, there is
                // nothing to serve: the app shows the step that is missing.
                let Some((name, address)) = status.serving() else {
                    return Ok(());
                };
                let config =
                    private_caddy_config(&settings, &deployments.running_ports(), name, address);
                (config, Some(self.tailscale.socket_dir()))
            }
        };
        let body = serde_json::to_string_pretty(&config).map_err(|e| e.to_string())?;
        let mount = socket_dir
            .as_deref()
            .map(|dir| dir.display().to_string())
            .unwrap_or_default();
        // Caddy reaches Tailscale's socket only if it was started with it.
        let running = caddy_running() && caddy_mount().as_deref() == Some(mount.as_str());
        if running && applied.as_deref() == Some(body.as_str()) {
            return Ok(());
        }
        fs::create_dir_all(&self.caddy_dir)
            .map_err(|e| format!("create {}: {e}", self.caddy_dir.display()))?;
        write_private(&self.caddy_dir.join(CADDY_CONFIG), body.as_bytes())?;
        if running {
            reload_caddy()?;
        } else {
            start_caddy(&self.caddy_dir, socket_dir.as_deref())?;
        }
        *applied = Some(body);
        Ok(())
    }
}

/// Keeps a domain's provider, giving it a gate of its own the first time.
fn connect(settings: &mut Settings, domain: &str, provider: Provider) -> Result<(), String> {
    let used: Vec<Gate> = settings
        .domains
        .iter()
        .filter(|other| other.name != domain && other.sign_in.is_some())
        .map(|other| other.gate)
        .collect();
    let entry = settings.domain_mut(Some(domain))?;
    if entry.sign_in.is_none() || used.contains(&entry.gate) {
        entry.gate = Gate::free(&used)?;
    }
    entry.sign_in = Some(provider);
    Ok(())
}

/// The apps page's address name unless the owner chose another; `apps` is
/// kept from apps for it.
const APPS_PAGE_NAME: &str = "apps";

/// A name for the apps page: an app's rules, except that `apps` is its own
/// and it cannot be the domain itself.
fn valid_page_name(name: &str) -> Result<String, String> {
    let trimmed = name.trim().to_ascii_lowercase();
    if trimmed == APPS_PAGE_NAME {
        return Ok(trimmed);
    }
    if trimmed.is_empty() {
        return Err("the apps page needs a name, such as apps".into());
    }
    valid_name(name)
}

/// The apps page may take a name no app has on its domain, that points at
/// this server: one the company uses elsewhere has its own record, which
/// the wildcard does not cover.
fn name_is_free(settings: &Settings, name: &str, domain: &str) -> Result<(), String> {
    let address = Address {
        domain: domain.to_string(),
        name: name.to_string(),
    };
    if settings
        .apps
        .values()
        .any(|route| route.addresses().contains(&address))
    {
        return Err(format!(
            "{name} is already an app's address. Choose another name."
        ));
    }
    points_here(name, domain)
}

/// With a wildcard, a domain is kept once `*.<domain>` points here.
fn wildcard_points_here(domain: &str, records: Records) -> Result<(), String> {
    let here = server_address().ok_or("this server's address is unknown")?;
    if records == Records::Wildcard
        && lookup(&format!("commitarium-check.{domain}"), here) != Lookup::Here
    {
        return Err(format!(
            "*.{domain} does not point at this server yet. At your DNS provider, add an \
             A record for *.{domain} with the value {here}, then try again. New records \
             can take a few minutes. Or add a record for each address instead."
        ));
    }
    Ok(())
}

/// Where a name under the domain points.
#[derive(Debug, PartialEq)]
enum Lookup {
    Here,
    /// Somewhere else: the name is used elsewhere.
    Elsewhere,
    /// Nowhere yet: it needs its record.
    Missing,
}

/// Whether name.domain points here, and if not, what to do about it. The
/// empty name is the domain itself, whose record is named `@`.
fn points_here(name: &str, domain: &str) -> Result<(), String> {
    let here = server_address().ok_or("this server's address is unknown")?;
    let host = host(name, domain);
    let record = match name {
        "" => format!("{domain} (the name @)"),
        _ => host.clone(),
    };
    match lookup(&host, here) {
        Lookup::Here => Ok(()),
        Lookup::Elsewhere => Err(format!(
            "{host} points somewhere else, so it is used elsewhere. Choose another name, or \
             point it at this server, {here}."
        )),
        Lookup::Missing => Err(format!(
            "{host} does not point at this server yet. At your DNS provider, add an A \
             record for {record} with the value {here}, then try again. New records can \
             take a few minutes."
        )),
    }
}

#[cfg(not(test))]
fn lookup(host: &str, here: IpAddr) -> Lookup {
    match (host, 443).to_socket_addrs() {
        Ok(mut addresses) => {
            if addresses.any(|address| address.ip() == here) {
                Lookup::Here
            } else {
                Lookup::Elsewhere
            }
        }
        Err(_) => Lookup::Missing,
    }
}

/// Tests have no DNS: a name under `elsewhere-` is used elsewhere, and one
/// under `missing-` has no record.
#[cfg(test)]
fn lookup(host: &str, _here: IpAddr) -> Lookup {
    if host.starts_with("elsewhere-") {
        Lookup::Elsewhere
    } else if host.starts_with("missing-") {
        Lookup::Missing
    } else {
        Lookup::Here
    }
}

/// What an apps page lists. At a domain, its page lists the apps with an
/// address there, each at that address. Behind company sign-in, a person
/// sees only what they can open: public apps, and company apps open to
/// everyone or to one of their groups. Hiding is a courtesy: each app keeps
/// its own gate.
fn catalog(
    settings: &Settings,
    running: &BTreeMap<String, u16>,
    groups: Option<&str>,
    base: Base,
    host: Option<&str>,
) -> Option<Vec<CatalogApp>> {
    let (page, domain) = match base {
        Base::Tailnet { .. } => (settings.private_apps_page.as_ref()?, None),
        Base::Internet => {
            let domain = settings.page_domain(host?)?;
            (domain.apps_page.as_ref()?, Some(domain.name.as_str()))
        }
    };
    let theirs: Vec<&str> = groups
        .unwrap_or_default()
        .split(',')
        .map(str::trim)
        .filter(|group| !group.is_empty())
        .collect();
    let mut apps: Vec<CatalogApp> = settings
        .apps
        .iter()
        .filter(|(_, route)| route.listed)
        .filter(|(_, route)| {
            page.access != Access::Company
                || match route.access {
                    Access::Public => true,
                    Access::Private => false,
                    Access::Company => {
                        route.groups.is_empty()
                            || route
                                .groups
                                .iter()
                                .any(|group| theirs.contains(&group.as_str()))
                    }
                }
        })
        .filter_map(|(project_id, route)| {
            let (name, url) = match domain {
                Some(domain) => {
                    let address = route.address_on(domain)?;
                    (address.name.clone(), format!("https://{}", address.host()))
                }
                None => (route.name.clone(), base.app_url(route)),
            };
            Some(CatalogApp {
                name,
                url,
                access: route.access,
                live: running.contains_key(project_id),
            })
        })
        .collect();
    apps.sort_by(|a, b| a.name.cmp(&b.name));
    Some(apps)
}

/// The apps page's route: its own gate or password, as chosen, then the
/// host's page, which reads which domain's page it is from the Host header
/// Caddy passes on.
fn apps_page_route(apps_page: &AppsPage, host: &str, gate: Option<(&str, Gate)>) -> Value {
    let mut handle = vec![sign_in::strip_identity()];
    match (apps_page.access, &apps_page.password_hash, gate) {
        (Access::Company, _, None) => {
            return json!({"match": [{"host": [host]}], "handle": [page(503, NO_SIGN_IN)], "terminal": true});
        }
        (Access::Company, _, Some((domain, gate))) => {
            handle.push(sign_in::caddy_gate(domain, gate, &[], page(403, NOT_YOURS)));
        }
        (Access::Private, Some(hash), _) => handle.push(basic_auth(&apps_page.name, hash)),
        _ => {}
    }
    handle.push(json!({
        "handler": "reverse_proxy",
        "upstreams": [{"dial": format!("127.0.0.1:{}", crate::apps_page::PORT)}],
    }));
    json!({"match": [{"host": [host]}], "handle": handle, "terminal": true})
}

/// Lets only the apps pages show a listed app in a frame. Anything the app
/// says about framing itself still counts: browsers apply every policy.
fn framed_by(page_origins: &[String]) -> Value {
    json!({
        "handler": "headers",
        "response": {
            "delete": ["X-Frame-Options"],
            "add": {"Content-Security-Policy": [
                format!("frame-ancestors 'self' {}", page_origins.join(" "))
            ]},
            "deferred": true,
        },
    })
}

fn basic_auth(realm: &str, hash: &str) -> Value {
    json!({
        "handler": "authentication",
        "providers": {"http_basic": {
            "hash": {"algorithm": "bcrypt"},
            "realm": realm,
            "accounts": [{
                "username": realm,
                "password": base64::engine::general_purpose::STANDARD.encode(hash),
            }],
        }},
    })
}

/// A name under a domain; the empty name is the domain itself.
fn host(name: &str, domain: &str) -> String {
    match name {
        "" => domain.to_string(),
        _ => format!("{name}.{domain}"),
    }
}

fn address(name: &str, domain: &str) -> String {
    format!("https://{}", host(name, domain))
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

/// An app's name under its domain. Empty is the domain itself, for a
/// website.
fn valid_name(name: &str) -> Result<String, String> {
    let name = name.trim().to_ascii_lowercase();
    if name.is_empty() {
        return Ok(name);
    }
    let valid = name.len() <= 40
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
/// VPS, which is where the domains' records must point.
#[cfg(not(test))]
fn server_address() -> Option<IpAddr> {
    let socket = std::net::UdpSocket::bind("0.0.0.0:0").ok()?;
    socket.connect("1.1.1.1:53").ok()?;
    Some(socket.local_addr().ok()?.ip())
}

#[cfg(test)]
fn server_address() -> Option<IpAddr> {
    Some(IpAddr::from([203, 0, 113, 7]))
}

/// The names company sign-in serves on, besides the apps': with a record
/// for each name, each needs its own.
fn sign_in_names_point_here(domain: &Domain, names: &[&str]) -> Result<(), String> {
    match domain.records {
        Records::EachName => names
            .iter()
            .try_for_each(|name| points_here(name, &domain.name)),
        Records::Wildcard => Ok(()),
    }
}

/// One app's route at one domain: who may open it, then the app, or the
/// "not running" page. `gate` is that domain's company sign-in.
fn app_route(
    project_id: &str,
    route: &AppRoute,
    running: &BTreeMap<String, u16>,
    hosts: &[String],
    gate: Option<(&str, Gate)>,
    page_origins: &[String],
) -> Value {
    let mut handle = vec![sign_in::strip_identity()];
    if route.access == Access::Company {
        // No provider would be no gate: then no way in.
        let Some((domain, gate)) = gate else {
            let page = page(503, NO_SIGN_IN);
            return json!({"match": [{"host": hosts}], "handle": [page], "terminal": true});
        };
        handle.push(sign_in::caddy_gate(
            domain,
            gate,
            &route.groups,
            page(403, NOT_YOURS),
        ));
    }
    if let (Access::Private, Some(hash)) = (route.access, &route.password_hash) {
        // A website at the bare domain has no name: people sign in as the
        // domain itself.
        let user = if route.name.is_empty() {
            &route.domain
        } else {
            &route.name
        };
        handle.push(basic_auth(user, hash));
    }
    if route.listed && !page_origins.is_empty() {
        handle.insert(1, framed_by(page_origins));
    }
    match running.get(project_id) {
        Some(port) => {
            let mut proxy = json!({
                "handler": "reverse_proxy",
                "upstreams": [{"dial": format!("127.0.0.1:{port}")}],
            });
            // Development servers answer only their own name. An app with
            // several addresses gets the one it was opened on, as a website
            // that picks its language by address needs; Caddy passes it as
            // it came, and sets X-Forwarded-Host too.
            if route.aliases.is_empty() {
                proxy["headers"] = json!({"request": {"set": {"Host": ["localhost"]}}});
            }
            handle.push(proxy);
        }
        None => handle.push(page(503, NOT_RUNNING)),
    }
    json!({"match": [{"host": hosts}], "handle": handle, "terminal": true})
}

/// Sends `www.<domain>` on to the bare domain, keeping the path and query.
fn www_redirect(www: &str, bare: &str) -> Value {
    json!({
        "match": [{"host": [www]}],
        "handle": [{
            "handler": "static_response",
            "status_code": 308,
            "headers": {"Location": [format!("https://{bare}{{http.request.uri}}")]},
        }],
        "terminal": true,
    })
}

/// Caddy's whole configuration. A published app with a running deployment
/// gets a route at each of its domains, behind its password when private;
/// one without gets the "not running" page. Any other name gets "No app
/// here". Caddy gets a certificate for each name a route matches.
fn caddy_config(
    settings: &Settings,
    running: &BTreeMap<String, u16>,
    authentik_home: Option<&str>,
) -> Value {
    let mut routes = Vec::new();
    if let Some(home) = authentik_home {
        routes.push(json!({
            "match": [{"host": [format!("{}.{home}", authentik::NAME)]}],
            "handle": [{
                "handler": "reverse_proxy",
                "upstreams": [{"dial": format!("127.0.0.1:{}", authentik::PORT)}],
            }],
            "terminal": true,
        }));
    }
    for domain in &settings.domains {
        if let Some(provider) = &domain.sign_in {
            routes.push(sign_in::caddy_route(&domain.name, provider, domain.gate));
        }
    }
    for domain in &settings.domains {
        if let (Some(page), Some(host)) = (&domain.apps_page, domain.page_host()) {
            routes.push(apps_page_route(page, &host, domain.gate()));
        }
    }
    for route in settings.apps.values() {
        for (www, bare) in route.www_redirects() {
            routes.push(www_redirect(&www, &bare));
        }
    }
    for (project_id, route) in &settings.apps {
        let origins = settings.page_origins(route);
        for name in route.domains() {
            let Some(domain) = settings.domains.iter().find(|domain| domain.name == name) else {
                continue;
            };
            let hosts: Vec<String> = route
                .addresses()
                .iter()
                .filter(|address| address.domain == name)
                .map(Address::host)
                .collect();
            routes.push(app_route(
                project_id,
                route,
                running,
                &hosts,
                domain.gate(),
                &origins,
            ));
        }
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

/// Caddy's whole configuration on the private network: the apps page on the
/// Tailscale name, and each app on its own port of it, all listening on the
/// Tailscale address alone. Caddy gets the name's certificate from
/// Tailscale, through its socket.
fn private_caddy_config(
    settings: &Settings,
    running: &BTreeMap<String, u16>,
    name: &str,
    tailnet_address: &str,
) -> Value {
    let server = |port: u16, routes: Vec<Value>| {
        json!({
            "listen": [format!("{tailnet_address}:{port}")],
            "routes": routes,
            // Nothing on port 80: the private network needs no redirects
            // or certificate challenges.
            "automatic_https": {"disable_redirects": true},
            "errors": {"routes": [{
                "match": [{"expression": "{http.error.status_code} >= 502"}],
                "handle": [page(503, NOT_RUNNING)],
            }]},
        })
    };
    let origins: Vec<String> = settings
        .private_apps_page
        .iter()
        .map(|_| format!("https://{name}"))
        .collect();
    let mut home = Vec::new();
    match &settings.private_apps_page {
        Some(page) => home.push(apps_page_route(page, name, None)),
        // The bare name is where people look for their apps.
        None => home.push(json!({"handle": [page(404, APPS_PAGE_OFF)]})),
    }
    let mut servers = serde_json::Map::new();
    servers.insert("apps".into(), server(443, home));
    let hosts = [name.to_string()];
    for (project_id, route) in &settings.apps {
        let Some(port) = route.port else { continue };
        // Extra addresses are the internet's: here it is one name.
        let plain = AppRoute {
            aliases: Vec::new(),
            ..route.clone()
        };
        let route = app_route(project_id, &plain, running, &hosts, None, &origins);
        servers.insert(format!("app-{port}"), server(port, vec![route]));
    }
    json!({
        "admin": {"listen": CADDY_ADMIN},
        "apps": {"http": {"servers": servers}},
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
const APPS_PAGE_OFF: &str =
    "The apps page is off. Turn it on in Commitarium under Settings → Server → Apps page.";
const NOT_YOURS: &str = "You are signed in, but this app is for other groups at your company.";
const NO_SIGN_IN: &str = "This app opens with company sign-in, which is not set up.";
const NO_PROVIDER: &str = "connect your company's sign-in in Settings first";
const NO_DOMAIN: &str = "set this server's domain in Settings first";
const PRIVATE_NO_COMPANY: &str =
    "company sign-in needs your own domain on the internet: on a private network, choose a password or anyone";

fn caddy_running() -> bool {
    docker::docker_command()
        .args(["inspect", "-f", "{{.State.Running}}", CADDY_CONTAINER])
        .output()
        .is_ok_and(|output| output.status.success() && output.stdout.starts_with(b"true"))
}

/// Which Tailscale socket directory Caddy was started with; empty for none.
const CADDY_MOUNT_LABEL: &str = "commitarium.tailscale-socket";

fn caddy_mount() -> Option<String> {
    let output = docker::docker_command()
        .args([
            "inspect",
            "-f",
            &format!("{{{{index .Config.Labels \"{CADDY_MOUNT_LABEL}\"}}}}"),
            CADDY_CONTAINER,
        ])
        .output()
        .ok()?;
    output
        .status
        .success()
        .then(|| String::from_utf8_lossy(&output.stdout).trim().to_string())
}

fn start_caddy(dir: &Path, tailscale_socket: Option<&Path>) -> Result<(), String> {
    let _ = docker::docker_command()
        .args(["rm", "-f", CADDY_CONTAINER])
        .output();
    let mount = format!("{}:/etc/commitarium:ro", dir.display());
    let socket = tailscale_socket
        .map(|dir| dir.display().to_string())
        .unwrap_or_default();
    let mut command = docker::docker_command();
    command.args([
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
        "--label",
        &format!("{CADDY_MOUNT_LABEL}={socket}"),
    ]);
    if !socket.is_empty() {
        // Where Caddy asks Tailscale for the name's certificate.
        command.args(["-v", &format!("{socket}:/var/run/tailscale")]);
    }
    let output = command
        .args([CADDY_IMAGE, "caddy", "run", "--config"])
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

    #[test]
    fn a_backend_may_take_api_as_its_name() {
        assert_eq!(valid_name("API").unwrap(), "api");
        assert!(valid_name("admin")
            .unwrap_err()
            .contains("kept for other uses"));
    }

    use super::*;

    fn app(domain: &str, name: &str, access: Access) -> AppRoute {
        AppRoute {
            domain: domain.into(),
            name: name.into(),
            www: true,
            aliases: Vec::new(),
            access,
            password_hash: None,
            password: None,
            groups: Vec::new(),
            listed: true,
            port: None,
        }
    }

    fn settings() -> Settings {
        let mut settings = Settings {
            domains: vec![Domain::new("example.com".into(), Records::Wildcard)],
            ..Settings::default()
        };
        settings.apps.insert(
            "prj_todo".into(),
            AppRoute {
                password_hash: Some("$2b$12$hash".into()),
                password: Some("tasks".into()),
                ..app("example.com", "todo", Access::Private)
            },
        );
        settings.apps.insert(
            "prj_blog".into(),
            app("example.com", "blog", Access::Public),
        );
        settings
    }

    /// The fixture with a second domain, example.org.
    fn two_domains() -> Settings {
        let mut settings = settings();
        settings
            .domains
            .push(Domain::new("example.org".into(), Records::EachName));
        settings
    }

    fn first(settings: &mut Settings) -> &mut Domain {
        &mut settings.domains[0]
    }

    #[test]
    fn a_running_app_is_proxied_and_a_private_one_asks_first() {
        let running = BTreeMap::from([("prj_todo".to_string(), 32778)]);
        let config = caddy_config(&settings(), &running, None);
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
        // One address: the development server's own name.
        assert_eq!(
            todo["handle"][2]["headers"]["request"]["set"]["Host"][0],
            "localhost"
        );
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
        // The bare domain, for a website.
        assert_eq!(valid_name(" ").unwrap(), "");
        assert!(valid_name("www").is_err());
        assert!(valid_name("sign-in").is_err());
        assert!(valid_name("auth").is_err());
        assert!(valid_name("-todo").is_err());
        assert!(valid_name("to do").is_err());
        assert!(valid_page_name("").is_err());
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
        let config = caddy_config(&settings, &running, None);
        let todo = route(&config, "todo.example.com");
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
            .find(|route| {
                route["match"][0]["host"]
                    .as_array()
                    .is_some_and(|hosts| hosts.iter().any(|h| h == host))
            })
            .unwrap_or_else(|| panic!("no route for {host}"))
    }

    fn has_route(config: &Value, host: &str) -> bool {
        config["apps"]["http"]["servers"]["public"]["routes"]
            .as_array()
            .unwrap()
            .iter()
            .any(|route| {
                route["match"][0]["host"]
                    .as_array()
                    .is_some_and(|hosts| hosts.iter().any(|h| h == host))
            })
    }

    #[test]
    fn a_company_app_is_gated_and_its_sign_in_address_served() {
        let mut settings = settings();
        let todo = settings.apps.get_mut("prj_todo").unwrap();
        todo.access = Access::Company;
        todo.groups = vec!["sales".into()];
        first(&mut settings).sign_in = Some(provider());
        let running = BTreeMap::from([("prj_todo".to_string(), 32778)]);
        let config = caddy_config(&settings, &running, None);
        let todo = route(&config, "todo.example.com");
        assert_eq!(todo["handle"][0]["handler"], "headers");
        assert_eq!(
            todo["handle"][1]["rewrite"]["uri"],
            "/oauth2/auth?allowed_groups=sales"
        );
        assert_eq!(todo["handle"][1]["upstreams"][0]["dial"], "127.0.0.1:4180");
        assert_eq!(todo["handle"][2]["upstreams"][0]["dial"], "127.0.0.1:32778");
        let sign_in = route(&config, "sign-in.example.com");
        assert_eq!(
            sign_in["handle"][0]["routes"][1]["handle"][0]["upstreams"][0]["dial"],
            "127.0.0.1:4180"
        );
        // authentik on this server gets its own address.
        let config = caddy_config(&settings, &running, Some("example.com"));
        let auth = route(&config, "auth.example.com");
        assert_eq!(auth["handle"][0]["upstreams"][0]["dial"], "127.0.0.1:9400");
        // Without a provider there is no way in, not an open door.
        first(&mut settings).sign_in = None;
        let config = caddy_config(&settings, &running, None);
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
        first(&mut settings).sign_in = Some(provider());
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
            .disconnect_sign_in(None)
            .unwrap_err()
            .contains("blog"));
        publishing.set_access("prj_blog", Access::Public).unwrap();
        publishing.disconnect_sign_in(None).unwrap();
        assert!(publishing.read().unwrap().domains[0].sign_in.is_none());
    }

    fn provider() -> Provider {
        Provider {
            issuer: "https://login.example.com".into(),
            client_id: "commitarium".into(),
            client_secret: "secret".into(),
            scopes: None,
            groups_claim: None,
            cookie_secret: "c".repeat(32),
            end_session: None,
        }
    }

    /// todo (password), blog (public), plus sales-only and team-wide company
    /// apps, all running.
    fn company_settings() -> (Settings, BTreeMap<String, u16>) {
        let mut settings = settings();
        first(&mut settings).sign_in = Some(provider());
        for (id, name, groups) in [
            ("prj_crm", "crm", vec!["sales".to_string()]),
            ("prj_wiki", "wiki", vec![]),
        ] {
            settings.apps.insert(
                id.into(),
                AppRoute {
                    groups,
                    ..app("example.com", name, Access::Company)
                },
            );
        }
        let running = settings.apps.keys().map(|id| (id.clone(), 3000)).collect();
        (settings, running)
    }

    fn names(apps: &[CatalogApp]) -> Vec<&str> {
        apps.iter().map(|app| app.name.as_str()).collect()
    }

    fn page_at(name: &str, access: Access) -> Option<AppsPage> {
        Some(AppsPage {
            name: name.into(),
            access,
            password_hash: None,
            password: None,
        })
    }

    const PAGE: Option<&str> = Some("apps.example.com");

    #[test]
    fn the_apps_page_lists_only_listed_apps_and_is_off_until_turned_on() {
        let (mut settings, running) = company_settings();
        assert!(catalog(&settings, &running, None, Base::Internet, PAGE).is_none());
        first(&mut settings).apps_page = page_at("apps", Access::Public);
        settings.apps.get_mut("prj_crm").unwrap().listed = false;
        let apps = catalog(&settings, &running, None, Base::Internet, PAGE).unwrap();
        // Open to anyone: every listed app, locks and all.
        assert_eq!(names(&apps), ["blog", "todo", "wiki"]);
        assert_eq!(apps[1].url, "https://todo.example.com");
        assert_eq!(apps[1].access, Access::Private);
        // Asked at no page's address: no page.
        assert!(catalog(
            &settings,
            &running,
            None,
            Base::Internet,
            Some("x.example.com")
        )
        .is_none());
    }

    #[test]
    fn behind_company_sign_in_each_person_sees_what_they_can_open() {
        let (mut settings, running) = company_settings();
        first(&mut settings).apps_page = page_at("apps", Access::Company);
        // No password apps: the page cannot know who has the password.
        let outsider = catalog(&settings, &running, Some("it"), Base::Internet, PAGE).unwrap();
        assert_eq!(names(&outsider), ["blog", "wiki"]);
        let seller = catalog(&settings, &running, Some("it, sales"), Base::Internet, PAGE).unwrap();
        assert_eq!(names(&seller), ["blog", "crm", "wiki"]);
        let nobody = catalog(&settings, &running, None, Base::Internet, PAGE).unwrap();
        assert_eq!(names(&nobody), ["blog", "wiki"]);
    }

    #[test]
    fn the_apps_page_is_served_behind_its_gate_and_frames_only_listed_apps() {
        let (mut settings, running) = company_settings();
        settings.apps.get_mut("prj_crm").unwrap().listed = false;
        // Off: no route, and no app gets the framing rule.
        let config = caddy_config(&settings, &running, None);
        assert!(config.to_string().find("apps.example.com").is_none());
        first(&mut settings).apps_page = page_at("apps", Access::Company);
        let config = caddy_config(&settings, &running, None);
        let page = route(&config, "apps.example.com");
        assert_eq!(page["handle"][0]["handler"], "headers");
        // Anyone at the company: the gate with no groups.
        assert_eq!(page["handle"][1]["rewrite"]["uri"], "/oauth2/auth");
        assert_eq!(
            page["handle"][2]["upstreams"][0]["dial"],
            format!("127.0.0.1:{}", crate::apps_page::PORT)
        );
        // The page reads which domain it is from the Host Caddy passes on.
        assert!(page["handle"][2].get("headers").is_none());
        let wiki = route(&config, "wiki.example.com");
        assert_eq!(
            wiki["handle"][1]["response"]["add"]["Content-Security-Policy"][0],
            "frame-ancestors 'self' https://apps.example.com"
        );
        let crm = route(&config, "crm.example.com");
        assert!(crm.to_string().find("frame-ancestors").is_none());
        // Behind a password, as a private app is.
        first(&mut settings).apps_page = Some(AppsPage {
            name: "apps".into(),
            access: Access::Private,
            password_hash: Some("$2b$12$hash".into()),
            password: Some("apps".into()),
        });
        let config = caddy_config(&settings, &running, None);
        let page = route(&config, "apps.example.com");
        assert_eq!(page["handle"][1]["handler"], "authentication");
        assert_eq!(
            page["handle"][1]["providers"]["http_basic"]["accounts"][0]["username"],
            "apps"
        );
    }

    #[test]
    fn turning_the_apps_page_on_checks_its_access_and_keeps_its_password() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        publishing.write(&settings()).unwrap();
        assert_eq!(
            publishing
                .set_apps_page(None, Some(Access::Company), None, None)
                .unwrap_err(),
            NO_PROVIDER
        );
        assert!(publishing
            .set_apps_page(None, Some(Access::Private), None, None)
            .is_err());
        publishing
            .set_apps_page(None, Some(Access::Private), Some("open sesame"), None)
            .unwrap();
        // Opening it to anyone, then closing it again, needs no new password.
        publishing
            .set_apps_page(None, Some(Access::Public), None, None)
            .unwrap();
        publishing
            .set_apps_page(None, Some(Access::Private), None, None)
            .unwrap();
        assert_eq!(
            publishing.apps_page_password(None).unwrap().as_deref(),
            Some("open sesame")
        );
        publishing.set_listed("prj_todo", false).unwrap();
        assert!(!publishing.read().unwrap().apps["prj_todo"].listed);
        publishing.set_apps_page(None, None, None, None).unwrap();
        assert!(publishing.read().unwrap().domains[0].apps_page.is_none());
    }

    #[test]
    fn a_domain_works_with_a_wildcard_or_a_record_for_each_name() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        // With a wildcard, it is checked before the domain is kept.
        publishing
            .set_domain("example.com", Records::Wildcard)
            .unwrap();
        // Without one, each name is checked as it is used, and a name with
        // no record yet says which record to add.
        publishing
            .set_domain("missing-records.test", Records::EachName)
            .unwrap();
        let mut settings = publishing.read().unwrap();
        assert_eq!(settings.domains.len(), 1);
        assert_eq!(settings.domains[0].records, Records::EachName);
        settings.domains[0].name = "example.com".into();
        publishing.write(&settings).unwrap();
        assert!(publishing
            .check_apps_page_name(None, "missing-apps")
            .unwrap_err()
            .contains("add an A record for missing-apps.example.com"));
        publishing.check_apps_page_name(None, "apps").unwrap();
        // Company sign-in's names need their records too.
        let mut domain = Domain::new("test".into(), Records::EachName);
        let names = ["missing-x"];
        assert!(sign_in_names_point_here(&domain, &names)
            .unwrap_err()
            .contains("missing-x.test"));
        domain.records = Records::Wildcard;
        assert!(sign_in_names_point_here(&domain, &names).is_ok());
    }

    #[test]
    fn changing_the_first_domain_moves_what_was_there() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        let mut settings = two_domains();
        settings.apps.get_mut("prj_blog").unwrap().aliases = vec![Address {
            domain: "example.com".into(),
            name: "news".into(),
        }];
        publishing.write(&settings).unwrap();
        assert!(publishing
            .set_domain("example.org", Records::Wildcard)
            .unwrap_err()
            .contains("already one of"));
        publishing
            .set_domain("example.net", Records::Wildcard)
            .unwrap();
        let settings = publishing.read().unwrap();
        assert_eq!(settings.domains[0].name, "example.net");
        assert_eq!(settings.domains[1].name, "example.org");
        assert_eq!(settings.apps["prj_blog"].domain, "example.net");
        assert_eq!(settings.apps["prj_blog"].aliases[0].domain, "example.net");
    }

    #[test]
    fn a_domain_in_use_is_kept_until_nothing_is_served_on_it() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        let mut settings = settings();
        publishing.write(&settings).unwrap();
        assert!(publishing
            .remove_domain(None)
            .unwrap_err()
            .contains("example.com is still in use by its apps"));
        settings.apps.clear();
        settings.domains[0].records = Records::EachName;
        publishing.write(&settings).unwrap();
        publishing.remove_domain(None).unwrap();
        assert!(publishing.read().unwrap().domains.is_empty());
    }

    #[test]
    fn a_second_domain_is_added_and_removed_only_when_unused() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        publishing.write(&settings()).unwrap();
        publishing
            .add_domain("Example.org", Records::EachName)
            .unwrap();
        assert!(publishing
            .add_domain("example.org", Records::Wildcard)
            .unwrap_err()
            .contains("already one of"));
        // With two, which one must be said.
        assert!(publishing
            .remove_domain(None)
            .unwrap_err()
            .contains("which"));
        publishing
            .publish(
                "prj_notes",
                "notes",
                Access::Public,
                None,
                on("example.org"),
            )
            .unwrap();
        assert!(publishing
            .remove_domain(Some("example.org"))
            .unwrap_err()
            .contains("its apps"));
        // An extra address counts as using it, too.
        publishing
            .publish(
                "prj_notes",
                "notes",
                Access::Public,
                None,
                Placing {
                    domain: Some("example.com".into()),
                    aliases: Some(vec![Address {
                        domain: "example.org".into(),
                        name: "notes".into(),
                    }]),
                    ..Placing::default()
                },
            )
            .unwrap();
        assert!(publishing
            .remove_domain(Some("example.org"))
            .unwrap_err()
            .contains("its apps"));
        publishing.unpublish("prj_notes").unwrap();
        publishing
            .set_apps_page(Some("example.org"), Some(Access::Public), None, None)
            .unwrap();
        assert!(publishing
            .remove_domain(Some("example.org"))
            .unwrap_err()
            .contains("apps page"));
        publishing
            .set_apps_page(Some("example.org"), None, None, None)
            .unwrap();
        publishing.remove_domain(Some("example.org")).unwrap();
        assert_eq!(publishing.read().unwrap().domains.len(), 1);
        assert!(publishing
            .remove_domain(Some("example.org"))
            .unwrap_err()
            .contains("not one of"));
    }

    fn on(domain: &str) -> Placing {
        Placing {
            domain: Some(domain.into()),
            ..Placing::default()
        }
    }

    #[test]
    fn where_the_server_is_is_kept() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        assert_eq!(publishing.read().unwrap().placement, None);
        publishing.set_placement(Placement::Home).unwrap();
        assert_eq!(publishing.read().unwrap().placement, Some(Placement::Home));
    }

    #[test]
    fn the_apps_page_can_take_another_name_that_is_free() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        publishing.write(&settings()).unwrap();
        // A name the company uses elsewhere, or an app's, is found first.
        assert!(publishing
            .check_apps_page_name(None, "elsewhere-apps")
            .unwrap_err()
            .contains("points somewhere else"));
        // A name with no record yet says which record to add.
        assert!(publishing
            .check_apps_page_name(None, "missing-apps")
            .unwrap_err()
            .contains("add an A record for missing-apps.example.com with the value 203.0.113.7"));
        assert!(publishing
            .check_apps_page_name(None, "todo")
            .unwrap_err()
            .contains("already an app's address"));
        assert!(publishing.check_apps_page_name(None, "www").is_err());
        publishing.check_apps_page_name(None, "apps").unwrap();
        publishing
            .set_apps_page(None, Some(Access::Public), None, Some("Tools"))
            .unwrap();
        let settings = publishing.read().unwrap();
        assert_eq!(
            settings.domains[0].apps_page.as_ref().unwrap().name,
            "tools"
        );
        // Changing who may open it keeps its name.
        publishing
            .set_apps_page(None, Some(Access::Private), Some("pw"), None)
            .unwrap();
        let settings = publishing.read().unwrap();
        assert_eq!(
            settings.domains[0].apps_page.as_ref().unwrap().name,
            "tools"
        );
        // Its route and the framing rule follow the name.
        let running = BTreeMap::from([("prj_blog".to_string(), 3000)]);
        let config = caddy_config(&settings, &running, None);
        route(&config, "tools.example.com");
        assert_eq!(
            route(&config, "blog.example.com")["handle"][1]["response"]["add"]
                ["Content-Security-Policy"][0],
            "frame-ancestors 'self' https://tools.example.com"
        );
        // An app cannot take the apps page's name.
        assert!(publishing
            .publish("prj_new", "tools", Access::Public, None, Placing::default())
            .map(|_| ())
            .unwrap_err()
            .contains("apps page"));
        // A page saved before names were chosen is at apps.
        let page: AppsPage = serde_json::from_str(r#"{"access":"public"}"#).unwrap();
        assert_eq!(page.name, "apps");
    }

    #[test]
    fn sign_in_stays_while_the_apps_page_uses_it() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        let mut settings = settings();
        first(&mut settings).sign_in = Some(provider());
        publishing.write(&settings).unwrap();
        publishing
            .set_apps_page(None, Some(Access::Company), None, None)
            .unwrap();
        assert!(publishing
            .disconnect_sign_in(None)
            .unwrap_err()
            .contains("apps page"));
    }

    #[test]
    fn an_app_saved_before_the_apps_page_is_listed() {
        let route: AppRoute = serde_json::from_str(r#"{"name":"todo","access":"public"}"#).unwrap();
        assert!(route.listed);
        assert!(route.www);
        let saved = serde_json::to_string(&route).unwrap();
        assert!(!saved.contains("listed"));
        assert!(!saved.contains("www"));
        assert!(!saved.contains("aliases"));
    }

    #[test]
    fn an_old_one_domain_file_reads_as_the_first_domain_and_saves_the_new_shape() {
        let old = serde_json::json!({
            "domain": "example.com",
            "records": "each_name",
            "sign_in": provider(),
            "apps_page": {"name": "tools", "access": "company"},
            "apps": {"prj_todo": {"name": "todo", "access": "public"}},
        });
        let settings: Settings = serde_json::from_value(old).unwrap();
        assert_eq!(settings.domains.len(), 1);
        let domain = &settings.domains[0];
        assert_eq!(domain.name, "example.com");
        assert_eq!(domain.records, Records::EachName);
        assert_eq!(domain.sign_in, Some(provider()));
        assert!(domain.gate.is_first());
        assert_eq!(domain.apps_page.as_ref().unwrap().name, "tools");
        // Its apps are at the domain they were at.
        assert_eq!(settings.apps["prj_todo"].domain, "example.com");
        assert!(settings.private_apps_page.is_none());
        let saved = serde_json::to_value(&settings).unwrap();
        for old in ["domain", "records", "sign_in", "apps_page"] {
            assert!(saved.get(old).is_none(), "{old} is still at the top");
        }
        assert_eq!(saved["domains"][0]["name"], "example.com");
        assert_eq!(saved["apps"]["prj_todo"]["domain"], "example.com");
        // And it reads back the same.
        let again: Settings = serde_json::from_value(saved).unwrap();
        assert_eq!(again.domains.len(), 1);
        assert_eq!(again.domains[0].sign_in, Some(provider()));
        // The gate the first domain had keeps running as it was.
        let config = caddy_config(&again, &BTreeMap::new(), None);
        let sign_in = route(&config, "sign-in.example.com");
        assert_eq!(
            sign_in["handle"][0]["routes"][1]["handle"][0]["upstreams"][0]["dial"],
            "127.0.0.1:4180"
        );
        // A homelab server's one apps page stays the private network's.
        let homelab: Settings = serde_json::from_value(serde_json::json!({
            "reach": "private",
            "apps_page": {"access": "public"},
        }))
        .unwrap();
        assert!(homelab.domains.is_empty());
        assert_eq!(homelab.private_apps_page.unwrap().name, "apps");
    }

    #[test]
    fn two_domains_get_their_own_routes_gates_and_apps_pages() {
        let mut settings = two_domains();
        let mut second_provider = provider();
        second_provider.client_id = "other-client".into();
        second_provider.cookie_secret = "d".repeat(32);
        settings.domains[0].sign_in = Some(provider());
        settings.domains[1].sign_in = Some(second_provider);
        settings.domains[1].gate = Gate::free(&[settings.domains[0].gate]).unwrap();
        settings.domains[0].apps_page = page_at("apps", Access::Company);
        settings.domains[1].apps_page = page_at("tools", Access::Company);
        settings
            .apps
            .insert("prj_crm".into(), app("example.org", "crm", Access::Company));
        let running = settings.apps.keys().map(|id| (id.clone(), 3000)).collect();
        let config = caddy_config(&settings, &running, None);
        // Each domain's sign-in address reaches its own gate.
        let one = route(&config, "sign-in.example.com");
        let two = route(&config, "sign-in.example.org");
        assert_eq!(
            one["handle"][0]["routes"][1]["handle"][0]["upstreams"][0]["dial"],
            "127.0.0.1:4180"
        );
        assert_eq!(
            two["handle"][0]["routes"][1]["handle"][0]["upstreams"][0]["dial"],
            "127.0.0.1:4181"
        );
        // A company app asks its own domain's gate.
        let crm = route(&config, "crm.example.org");
        assert_eq!(crm["handle"][2]["upstreams"][0]["dial"], "127.0.0.1:4181");
        // Each domain's apps page, behind its own gate.
        let page = route(&config, "tools.example.org");
        assert_eq!(page["handle"][1]["upstreams"][0]["dial"], "127.0.0.1:4181");
        route(&config, "apps.example.com");
        assert!(!has_route(&config, "tools.example.com"));
        // Each gate has its own client, cookie secret, cookie domain and port.
        let gates = settings.gates();
        assert_eq!(gates.len(), 2);
        let args: Vec<String> = gates
            .iter()
            .map(|(gate, provider, domain)| sign_in::arguments(provider, domain, *gate).join(" "))
            .collect();
        assert!(args[0].contains("--cookie-domain=.example.com"));
        assert!(args[1].contains("--cookie-domain=.example.org"));
        assert!(args[1].contains("--client-id=other-client"));
        assert!(args[1].contains("--http-address=127.0.0.1:4181"));
        assert_ne!(gates[0].1.cookie_secret, gates[1].1.cookie_secret);
        // Each page lists only its own domain's apps.
        let com = catalog(&settings, &running, None, Base::Internet, PAGE).unwrap();
        assert_eq!(names(&com), ["blog"]);
        let org = catalog(
            &settings,
            &running,
            None,
            Base::Internet,
            Some("tools.example.org:443"),
        )
        .unwrap();
        assert_eq!(names(&org), ["crm"]);
        assert_eq!(org[0].url, "https://crm.example.org");
    }

    #[test]
    fn connecting_a_second_domain_gives_it_its_own_gate() {
        let mut settings = two_domains();
        connect(&mut settings, "example.com", provider()).unwrap();
        connect(&mut settings, "example.org", provider()).unwrap();
        assert!(settings.domains[0].gate.is_first());
        assert_eq!(settings.domains[1].gate.port(), 4181);
        // Connecting again keeps the gate it has.
        connect(&mut settings, "example.org", provider()).unwrap();
        assert_eq!(settings.domains[1].gate.port(), 4181);
    }

    #[test]
    fn a_name_taken_on_one_domain_is_free_on_another_but_no_address_twice() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        publishing.write(&two_domains()).unwrap();
        // todo is example.com's: free at example.org.
        let published = publishing
            .publish("prj_todo2", "todo", Access::Public, None, on("example.org"))
            .unwrap();
        assert_eq!(published.url, "https://todo.example.org");
        assert!(publishing
            .publish("prj_other", "todo", Access::Public, None, on("example.org"))
            .map(|_| ())
            .unwrap_err()
            .contains("todo.example.org is already another app's address"));
        // Nor as an extra address.
        let alias = |domain: &str, name: &str| Placing {
            domain: Some("example.org".into()),
            aliases: Some(vec![Address {
                domain: domain.into(),
                name: name.into(),
            }]),
            ..Placing::default()
        };
        assert!(publishing
            .publish(
                "prj_other",
                "other",
                Access::Public,
                None,
                alias("example.com", "blog")
            )
            .map(|_| ())
            .unwrap_err()
            .contains("blog.example.com is already another app's address"));
        // Nor its own main address again.
        assert!(publishing
            .publish(
                "prj_other",
                "other",
                Access::Public,
                None,
                alias("example.org", "other")
            )
            .map(|_| ())
            .unwrap_err()
            .contains("given twice"));
        // Nor a domain's apps page.
        publishing
            .set_apps_page(Some("example.org"), Some(Access::Public), None, None)
            .unwrap();
        assert!(publishing
            .publish(
                "prj_other",
                "other",
                Access::Public,
                None,
                alias("example.org", "apps")
            )
            .map(|_| ())
            .unwrap_err()
            .contains("kept for other uses"));
        publishing
            .set_apps_page(
                Some("example.org"),
                Some(Access::Public),
                None,
                Some("tools"),
            )
            .unwrap();
        assert!(publishing
            .publish(
                "prj_other",
                "other",
                Access::Public,
                None,
                alias("example.org", "tools")
            )
            .map(|_| ())
            .unwrap_err()
            .contains("apps page's address"));
        // The apps page cannot take an app's alias either.
        publishing
            .publish(
                "prj_other",
                "other",
                Access::Public,
                None,
                alias("example.com", "more"),
            )
            .unwrap();
        assert!(publishing
            .check_apps_page_name(None, "more")
            .unwrap_err()
            .contains("already an app's address"));
        // But the same name at the other domain is free for a page.
        publishing
            .check_apps_page_name(Some("example.org"), "blog")
            .unwrap();
        // A domain the server does not have is refused.
        assert!(publishing
            .publish("prj_x", "x", Access::Public, None, on("example.net"))
            .map(|_| ())
            .unwrap_err()
            .contains("not one of this server's domains"));
        // Publishing again without a domain keeps the one it has.
        let again = publishing
            .publish(
                "prj_todo2",
                "todo",
                Access::Public,
                None,
                Placing::default(),
            )
            .unwrap();
        assert_eq!(again.url, "https://todo.example.org");
    }

    #[test]
    fn a_bare_domain_app_answers_there_and_www_sends_people_to_it() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        publishing.write(&settings()).unwrap();
        let published = publishing
            .publish("prj_site", "", Access::Public, None, Placing::default())
            .unwrap();
        assert_eq!(published.url, "https://example.com");
        let settings = publishing.read().unwrap();
        assert!(settings.apps["prj_site"].www);
        let running = BTreeMap::from([("prj_site".to_string(), 3000)]);
        let config = caddy_config(&settings, &running, None);
        let site = route(&config, "example.com");
        assert_eq!(site["handle"][1]["upstreams"][0]["dial"], "127.0.0.1:3000");
        let www = route(&config, "www.example.com");
        assert_eq!(www["handle"][0]["status_code"], 308);
        assert_eq!(
            www["handle"][0]["headers"]["Location"][0],
            "https://example.com{http.request.uri}"
        );
        // The bare name is taken once per domain.
        assert!(publishing
            .publish("prj_other", "", Access::Public, None, Placing::default())
            .map(|_| ())
            .unwrap_err()
            .contains("example.com is already another app's address"));
        // Without www, only the bare address.
        publishing
            .publish(
                "prj_site",
                "",
                Access::Public,
                None,
                Placing {
                    www: Some(false),
                    ..Placing::default()
                },
            )
            .unwrap();
        let settings = publishing.read().unwrap();
        let config = caddy_config(&settings, &running, None);
        assert!(!has_route(&config, "www.example.com"));
        // The apps page opens it at the bare address.
        let mut settings = settings;
        first(&mut settings).apps_page = page_at("apps", Access::Public);
        let apps = catalog(&settings, &running, None, Base::Internet, PAGE).unwrap();
        assert_eq!(apps[0].name, "");
        assert_eq!(apps[0].url, "https://example.com");
    }

    #[test]
    fn a_bare_domain_needs_its_own_record_even_with_a_wildcard() {
        let mut settings = Settings {
            domains: vec![Domain::new("missing-apex.test".into(), Records::Wildcard)],
            ..Settings::default()
        };
        settings.apps.insert(
            "prj_site".into(),
            app("missing-apex.test", "", Access::Public),
        );
        settings.apps.insert(
            "prj_notes".into(),
            app("missing-apex.test", "notes", Access::Public),
        );
        let problem = settings.record_problem(&settings.apps["prj_site"]).unwrap();
        assert!(
            problem.contains("add an A record for missing-apex.test (the name @)"),
            "{problem}"
        );
        // The wildcard covers a name under it.
        assert!(settings
            .record_problem(&settings.apps["prj_notes"])
            .is_none());
        // And publishing there says so too.
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        publishing.write(&settings).unwrap();
        assert!(publishing
            .publish("prj_site", "", Access::Public, None, Placing::default())
            .map(|_| ())
            .unwrap_err()
            .contains("(the name @)"));
        // www is checked too, when it is on.
        let site = app("example.com", "", Access::Public);
        let names: Vec<String> = site.dns_names().iter().map(Address::host).collect();
        assert_eq!(names, ["example.com", "www.example.com"]);
    }

    #[test]
    fn an_app_with_extra_addresses_gets_the_address_it_was_opened_on() {
        let mut settings = two_domains();
        settings.domains[1].apps_page = page_at("apps", Access::Public);
        first(&mut settings).apps_page = page_at("apps", Access::Public);
        settings.apps.insert(
            "prj_site".into(),
            AppRoute {
                aliases: vec![Address {
                    domain: "example.org".into(),
                    name: "".into(),
                }],
                ..app("example.com", "", Access::Public)
            },
        );
        let running: BTreeMap<String, u16> =
            settings.apps.keys().map(|id| (id.clone(), 3000)).collect();
        let config = caddy_config(&settings, &running, None);
        let main = route(&config, "example.com");
        let extra = route(&config, "example.org");
        for site in [main, extra] {
            let proxy = site["handle"].as_array().unwrap().last().unwrap();
            assert_eq!(proxy["upstreams"][0]["dial"], "127.0.0.1:3000");
            // No Host of its own: the one it was opened on passes through.
            assert!(proxy.get("headers").is_none());
            // Either domain's apps page may frame it.
            assert_eq!(
                site["handle"][1]["response"]["add"]["Content-Security-Policy"][0],
                "frame-ancestors 'self' https://apps.example.com https://apps.example.org"
            );
        }
        // Both bare addresses get their www.
        route(&config, "www.example.com");
        assert_eq!(
            route(&config, "www.example.org")["handle"][0]["headers"]["Location"][0],
            "https://example.org{http.request.uri}"
        );
        // A plain app keeps the development server's name.
        let blog = route(&config, "blog.example.com");
        let proxy = blog["handle"].as_array().unwrap().last().unwrap();
        assert_eq!(proxy["headers"]["request"]["set"]["Host"][0], "localhost");
        // Each domain's page lists it at that domain's address.
        let org = catalog(
            &settings,
            &running,
            None,
            Base::Internet,
            Some("apps.example.org"),
        )
        .unwrap();
        assert_eq!(org.len(), 1);
        assert_eq!(org[0].url, "https://example.org");
        // And the client sees every address.
        assert_eq!(
            Base::Internet.app_urls(&settings.apps["prj_site"]),
            ["https://example.com", "https://example.org"]
        );
    }

    #[test]
    fn company_access_needs_sign_in_on_every_domain_of_the_app() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        let mut settings = two_domains();
        first(&mut settings).sign_in = Some(provider());
        settings.apps.get_mut("prj_blog").unwrap().aliases = vec![Address {
            domain: "example.org".into(),
            name: "blog".into(),
        }];
        publishing.write(&settings).unwrap();
        assert!(publishing
            .set_access("prj_blog", Access::Company)
            .unwrap_err()
            .contains("for example.org"));
        publishing.set_access("prj_todo", Access::Company).unwrap();
        // The other domain's sign-in is not this one's to disconnect.
        connect(&mut settings, "example.org", provider()).unwrap();
        settings.apps.get_mut("prj_todo").unwrap().access = Access::Company;
        publishing.write(&settings).unwrap();
        publishing.disconnect_sign_in(Some("example.org")).unwrap();
        assert!(publishing
            .disconnect_sign_in(Some("example.com"))
            .unwrap_err()
            .contains("todo.example.com"));
    }

    #[test]
    fn the_apps_page_signs_out_at_its_own_domain() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        let mut settings = two_domains();
        connect(&mut settings, "example.com", provider()).unwrap();
        connect(&mut settings, "example.org", provider()).unwrap();
        settings.domains[0].apps_page = page_at("apps", Access::Company);
        settings.domains[1].apps_page = page_at("apps", Access::Public);
        publishing.write(&settings).unwrap();
        assert_eq!(
            publishing
                .apps_page_sign_out(Some("apps.example.com"))
                .unwrap()
                .as_deref(),
            Some("https://sign-in.example.com/sign-out")
        );
        // Open to anyone: nothing to sign out of there.
        assert_eq!(
            publishing
                .apps_page_sign_out(Some("apps.example.org"))
                .unwrap(),
            None
        );
    }

    #[test]
    fn on_the_private_network_apps_get_ports_on_the_tailscale_name() {
        let mut settings = settings();
        settings.reach = Reach::Private;
        settings.domains.clear();
        assign_ports(&mut settings);
        // Each app its own port, kept: a new app takes the next free one.
        assert_eq!(settings.apps["prj_blog"].port, Some(8001));
        assert_eq!(settings.apps["prj_todo"].port, Some(8002));
        settings.apps.remove("prj_blog");
        let mut new = settings.apps["prj_todo"].clone();
        new.name = "notes".into();
        new.port = None;
        settings.apps.insert("prj_notes".into(), new);
        assign_ports(&mut settings);
        assert_eq!(settings.apps["prj_todo"].port, Some(8002));
        assert_eq!(settings.apps["prj_notes"].port, Some(8001));

        let status = crate::tailscale::parse_status(
            &serde_json::json!({
                "BackendState": "Running",
                "Self": {"DNSName": "homelab.tail1234.ts.net.", "TailscaleIPs": ["100.64.0.7"]},
                "CertDomains": ["homelab.tail1234.ts.net"],
            }),
            true,
        );
        assert_eq!(
            settings
                .base(Some(&status))
                .unwrap()
                .app_url(&settings.apps["prj_todo"]),
            "https://homelab.tail1234.ts.net:8002"
        );
        // Not ready yet: nowhere to serve.
        assert!(settings.base(None).is_none());

        // Without the apps page, the bare name says how to turn it on.
        let off = private_caddy_config(
            &settings,
            &BTreeMap::new(),
            "homelab.tail1234.ts.net",
            "100.64.0.7",
        );
        let body = off["apps"]["http"]["servers"]["apps"]["routes"][0]["handle"][0]["body"]
            .as_str()
            .unwrap()
            .to_string();
        assert!(body.contains("The apps page is off"));

        settings.private_apps_page = Some(AppsPage {
            name: "apps".into(),
            access: Access::Private,
            password_hash: Some("$2b$12$hash".into()),
            password: Some("pw".into()),
        });
        // An extra address from the internet changes nothing here.
        settings.apps.get_mut("prj_todo").unwrap().aliases = vec![Address {
            domain: "example.org".into(),
            name: "todo".into(),
        }];
        let running = BTreeMap::from([("prj_todo".to_string(), 3000)]);
        let config =
            private_caddy_config(&settings, &running, "homelab.tail1234.ts.net", "100.64.0.7");
        let servers = &config["apps"]["http"]["servers"];
        // Only on the Tailscale address: nothing on the home network.
        assert_eq!(servers["apps"]["listen"][0], "100.64.0.7:443");
        assert_eq!(
            servers["apps"]["automatic_https"]["disable_redirects"],
            true
        );
        let page = &servers["apps"]["routes"][0];
        assert_eq!(page["match"][0]["host"][0], "homelab.tail1234.ts.net");
        assert_eq!(page["handle"][1]["handler"], "authentication");
        let todo = &servers["app-8002"];
        assert_eq!(todo["listen"][0], "100.64.0.7:8002");
        let route = &todo["routes"][0];
        assert_eq!(
            route["handle"][1]["response"]["add"]["Content-Security-Policy"][0],
            "frame-ancestors 'self' https://homelab.tail1234.ts.net"
        );
        assert_eq!(route["handle"][2]["handler"], "authentication");
        assert_eq!(route["handle"][3]["upstreams"][0]["dial"], "127.0.0.1:3000");
        assert_eq!(
            route["handle"][3]["headers"]["request"]["set"]["Host"][0],
            "localhost"
        );
        // The catalog links each app at its port, whatever the host.
        let base = settings.base(Some(&status)).unwrap();
        let apps = catalog(&settings, &running, None, base, None).unwrap();
        assert_eq!(apps[0].url, "https://homelab.tail1234.ts.net:8001");
    }

    #[test]
    fn going_private_needs_company_sign_in_gone_and_refuses_it_after() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        let mut settings = settings();
        first(&mut settings).sign_in = Some(provider());
        settings.reach = Reach::Private;
        publishing.write(&settings).unwrap();
        // Written straight to the file: set_reach would start Tailscale.
        let saved = publishing.read().unwrap();
        assert_eq!(saved.reach, Reach::Private);
        assert!(publishing
            .set_apps_page(None, Some(Access::Company), None, None)
            .unwrap_err()
            .contains("private network"));
        // The private network's page is its own, apart from the domain's.
        publishing
            .set_apps_page(None, Some(Access::Public), None, None)
            .unwrap();
        let saved = publishing.read().unwrap();
        assert!(saved.private_apps_page.is_some());
        assert!(saved.domains[0].apps_page.is_none());
        // Names there are labels: no bare name, no extra addresses.
        assert!(publishing
            .publish("prj_x", "", Access::Public, None, Placing::default())
            .map(|_| ())
            .is_err());
        assert!(publishing
            .publish(
                "prj_x",
                "x",
                Access::Public,
                None,
                Placing {
                    aliases: Some(vec![Address {
                        domain: "example.com".into(),
                        name: "y".into(),
                    }]),
                    ..Placing::default()
                }
            )
            .map(|_| ())
            .unwrap_err()
            .contains("extra addresses"));
        settings.reach = Reach::Internet;
        publishing.write(&settings).unwrap();
        assert!(publishing
            .set_reach(Reach::Private)
            .unwrap_err()
            .contains("disconnect company sign-in"));
        // A file from before homelab mode reads as the internet.
        let old: Settings = serde_json::from_str(r#"{"domain":"example.com"}"#).unwrap();
        assert_eq!(old.reach, Reach::Internet);
        assert!(!serde_json::to_string(&old).unwrap().contains("reach"));
    }

    #[test]
    fn on_the_private_network_a_removed_domain_leaves_its_apps_for_the_next() {
        let data = tempfile::tempdir().unwrap();
        let publishing = Publishing::new(data.path());
        let mut settings = settings();
        settings.reach = Reach::Private;
        publishing.write(&settings).unwrap();
        publishing.remove_domain(None).unwrap();
        let saved = publishing.read().unwrap();
        assert!(saved.domains.is_empty());
        assert_eq!(saved.apps["prj_todo"].domain, "");
        publishing
            .set_domain("example.org", Records::Wildcard)
            .unwrap();
        assert_eq!(
            publishing.read().unwrap().apps["prj_todo"].domain,
            "example.org"
        );
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
            let connection = Authentik::new(&dir)
                .install("example.test", &[], "example.test")
                .unwrap();
            Provider {
                issuer: connection.issuer,
                client_id: connection.client_id,
                client_secret: connection.client_secret,
                scopes: None,
                groups_claim: None,
                cookie_secret: "c".repeat(32),
                end_session: None,
            }
        } else {
            Provider {
                issuer: std::env::var("SIGN_IN_TEST_ISSUER").unwrap(),
                client_id: "commitarium".into(),
                client_secret: "secret".into(),
                scopes: None,
                groups_claim: None,
                cookie_secret: "c".repeat(32),
                end_session: None,
            }
        };
        let mut settings = Settings {
            domains: vec![Domain {
                sign_in: Some(provider.clone()),
                ..Domain::new("example.test".into(), Records::Wildcard)
            }],
            ..Settings::default()
        };
        settings.apps.insert(
            "prj_todo".into(),
            AppRoute {
                groups,
                ..app("example.test", "todo", Access::Company)
            },
        );
        let running = BTreeMap::from([("prj_todo".to_string(), 3000)]);
        let home = authentik.then_some("example.test");
        let config = caddy_config(&settings, &running, home);
        fs::write(dir.join("caddy.json"), config.to_string()).unwrap();
        fs::write(dir.join("client-secret"), &provider.client_secret).unwrap();
        fs::write(
            dir.join("gate-args"),
            sign_in::arguments(&provider, "example.test", Gate::default()).join("\n"),
        )
        .unwrap();
    }
}
