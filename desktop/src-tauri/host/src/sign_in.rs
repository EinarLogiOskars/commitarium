//! Company sign-in (COMPANY_SIGN_IN_PLAN.md, C1): the gate in front of the
//! apps whose access is the company's. It is oauth2-proxy, in a container on
//! the host's network that answers only on loopback; Caddy asks it about each
//! request to such an app (forward auth). It speaks OpenID Connect, so any
//! company provider fits: authentik, Pocket ID, Entra ID, Google, Okta.
//!
//! Nothing of it exists until a provider is connected: the container starts
//! then, and goes when the provider is disconnected.

use crate::docker;
use crate::runtime::block_on;
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use sha2::{Digest, Sha256};
use std::time::Duration;

/// The address the company's provider sends people back to: one per domain,
/// registered once with the provider.
pub const NAME: &str = "sign-in";
/// The first gate's port; each further gate takes the next one.
const PORT: u16 = 4180;
/// How many gates a server may run: one per domain with sign-in.
const MOST_GATES: u16 = 100;
const CONTAINER: &str = "commitarium-sign-in";
const IMAGE: &str = "quay.io/oauth2-proxy/oauth2-proxy:v7.12.0";
/// Which provider settings the running gate was started with.
const SPEC_LABEL: &str = "commitarium.sign-in.spec";
const DEFAULT_SCOPES: &str = "openid email profile";
/// A person who opens no app for this long signs in again, so a device left
/// signed in somewhere does not stay open for days. The provider's own
/// session lasts as long (authentik's is set so), or it would sign them
/// straight back in.
pub const SIGN_IN_LASTS_HOURS: u32 = 8;
/// How often the gate asks the provider whether a person may still sign in:
/// someone removed there, or moved out of a group, loses access within it.
const CHECK_EVERY: &str = "1h";
/// Who signed in, as the gate answers it, and as the app receives it. Caddy
/// removes the app's names from every request first, so none is forged.
const IDENTITY: &[(&str, &str)] = &[
    ("X-Auth-Request-User", "X-Commitarium-User"),
    ("X-Auth-Request-Email", "X-Commitarium-Email"),
    (
        "X-Auth-Request-Preferred-Username",
        "X-Commitarium-Username",
    ),
    ("X-Auth-Request-Groups", "X-Commitarium-Groups"),
];

/// One domain's gate (MULTI_DOMAIN_PLAN.md, M4): a cookie cannot cross
/// domains, so each domain with sign-in runs its own oauth2-proxy, on its own
/// loopback port. The first keeps the name and port it had before a server
/// had several domains, so an upgraded server's gate is left as it was.
#[derive(Clone, Copy, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
#[serde(transparent)]
pub struct Gate(u16);

impl Gate {
    pub fn is_first(&self) -> bool {
        self.0 == 0
    }

    pub fn port(self) -> u16 {
        PORT + self.0
    }

    fn container(self) -> String {
        match self.0 {
            0 => CONTAINER.to_string(),
            n => format!("{CONTAINER}-{n}"),
        }
    }

    /// The first gate no other domain uses.
    pub fn free(used: &[Gate]) -> Result<Gate, String> {
        (0..MOST_GATES)
            .map(Gate)
            .find(|gate| !used.contains(gate))
            .ok_or_else(|| format!("a server has company sign-in on at most {MOST_GATES} domains"))
    }
}

/// The company's provider, as the server keeps it.
#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct Provider {
    pub issuer: String,
    pub client_id: String,
    pub client_secret: String,
    /// The scopes asked for; `openid email profile` when unset. Providers
    /// that put groups in tokens only when asked need `groups` added.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub scopes: Option<String>,
    /// The token claim that lists a person's groups; `groups` when unset.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub groups_claim: Option<String>,
    /// Seals the sign-in cookie; made once, kept when the provider changes.
    pub cookie_secret: String,
    /// Where the provider ends its own session (OpenID Connect's
    /// `end_session_endpoint`), when it has one. Signing out goes there
    /// after the gate's cookie is cleared, or the provider would sign the
    /// person straight back in.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub end_session: Option<String>,
}

/// What a client sends to connect a provider.
#[derive(Deserialize)]
pub struct Connect {
    pub issuer: String,
    pub client_id: String,
    pub client_secret: String,
    #[serde(default)]
    pub scopes: Option<String>,
    #[serde(default)]
    pub groups_claim: Option<String>,
    /// The domain it signs people in for; the first when unset.
    #[serde(default)]
    pub domain: Option<String>,
}

/// The connected provider as a client sees it: never its secret.
#[derive(Serialize)]
pub struct Connected {
    pub issuer: String,
    pub client_id: String,
    pub scopes: String,
    pub groups_claim: String,
    /// The address to register with the provider.
    pub redirect_url: Option<String>,
    /// The address that signs a person out of every app on the server.
    pub sign_out_url: Option<String>,
}

impl Provider {
    /// Checks a provider before it is kept: its issuer must publish an
    /// OpenID Connect discovery document naming itself.
    pub fn connect(asked: Connect, cookie_secret: Option<String>) -> Result<Self, String> {
        let issuer = asked.issuer.trim().trim_end_matches('/').to_string();
        if !issuer.starts_with("https://") {
            return Err(
                "the issuer is an https:// address, such as https://login.example.com".into(),
            );
        }
        let client_id = asked.client_id.trim().to_string();
        let client_secret = asked.client_secret.trim().to_string();
        if client_id.is_empty() || client_secret.is_empty() {
            return Err("give the client ID and the client secret".into());
        }
        let blank = |value: Option<String>| {
            value
                .map(|value| value.trim().to_string())
                .filter(|value| !value.is_empty())
        };
        // Kept as the provider names itself: tokens carry it exactly, and
        // some (authentik) end it with a slash.
        let (issuer, end_session) = check_issuer(&issuer)?;
        Ok(Self {
            issuer,
            end_session,
            client_id,
            client_secret,
            scopes: blank(asked.scopes),
            groups_claim: blank(asked.groups_claim),
            cookie_secret: cookie_secret.map_or_else(new_cookie_secret, Ok)?,
        })
    }

    pub fn shown(&self, domain: Option<&str>) -> Connected {
        Connected {
            issuer: self.issuer.clone(),
            client_id: self.client_id.clone(),
            scopes: self.scopes().to_string(),
            groups_claim: self.groups_claim().to_string(),
            redirect_url: domain.map(redirect_url),
            sign_out_url: domain.map(sign_out_url),
        }
    }

    fn scopes(&self) -> &str {
        self.scopes.as_deref().unwrap_or(DEFAULT_SCOPES)
    }

    fn groups_claim(&self) -> &str {
        self.groups_claim.as_deref().unwrap_or("groups")
    }
}

fn redirect_url(domain: &str) -> String {
    format!("https://{NAME}.{domain}/oauth2/callback")
}

/// One address signs a person out of every app at the domain: the gate
/// clears its cookie, then the provider ends its own session.
pub fn sign_out_url(domain: &str) -> String {
    format!("https://{NAME}.{domain}/sign-out")
}

/// The gate's sign-out, sending the person on to the provider's.
fn gate_sign_out(provider: &Provider) -> String {
    match &provider.end_session {
        Some(end) => format!("/oauth2/sign_out?rd={}", encode(end)),
        None => "/oauth2/sign_out".into(),
    }
}

/// A URL as one query value.
pub(crate) fn encode(value: &str) -> String {
    value
        .bytes()
        .map(|byte| match byte {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'.' | b'_' | b'~' => {
                (byte as char).to_string()
            }
            _ => format!("%{byte:02X}"),
        })
        .collect()
}

/// The host part of an https:// address.
fn host_of(url: &str) -> Option<&str> {
    let rest = url.strip_prefix("https://")?;
    let host = rest.split(['/', '?', '#']).next()?;
    Some(host.split(':').next().unwrap_or(host)).filter(|host| !host.is_empty())
}

fn check_issuer(issuer: &str) -> Result<(String, Option<String>), String> {
    #[derive(Deserialize)]
    struct Discovery {
        issuer: String,
        authorization_endpoint: String,
        #[serde(default)]
        end_session_endpoint: Option<String>,
    }
    let url = format!("{issuer}/.well-known/openid-configuration");
    let discovery: Discovery = block_on(async {
        reqwest::Client::new()
            .get(&url)
            .timeout(Duration::from_secs(10))
            .send()
            .await
            .and_then(|response| response.error_for_status())
            .map_err(|e| format!("{issuer} does not answer as a sign-in provider: {e}"))?
            .json()
            .await
            .map_err(|_| format!("{issuer} does not answer as a sign-in provider"))
    })?;
    if discovery.issuer.trim_end_matches('/') != issuer
        || discovery.authorization_endpoint.is_empty()
    {
        return Err(format!(
            "{issuer} names itself {}: use that address as the issuer",
            discovery.issuer
        ));
    }
    let end_session = discovery
        .end_session_endpoint
        .filter(|end| host_of(end).is_some());
    Ok((discovery.issuer, end_session))
}

/// 32 random characters: oauth2-proxy takes a 32-byte secret as it is.
pub(crate) fn new_cookie_secret() -> Result<String, String> {
    const ALPHABET: &[u8] = b"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789";
    let mut bytes = [0u8; 32];
    getrandom::fill(&mut bytes).map_err(|e| format!("make the cookie secret: {e}"))?;
    Ok(bytes
        .iter()
        .map(|byte| ALPHABET[*byte as usize % ALPHABET.len()] as char)
        .collect())
}

/// The gate's settings, secrets aside: they reach it in its environment.
pub(crate) fn arguments(provider: &Provider, domain: &str, gate: Gate) -> Vec<String> {
    let mut arguments = vec![
        "--provider=oidc".into(),
        format!("--oidc-issuer-url={}", provider.issuer),
        format!("--client-id={}", provider.client_id),
        format!("--scope={}", provider.scopes()),
        format!("--oidc-groups-claim={}", provider.groups_claim()),
        format!("--redirect-url={}", redirect_url(domain)),
        format!("--cookie-domain=.{domain}"),
        format!("--whitelist-domain=.{domain}"),
        format!("--http-address=127.0.0.1:{}", gate.port()),
        "--cookie-secure=true".into(),
        "--cookie-name=_commitarium_sign_in".into(),
        // Each check renews the cookie, so only unused sign-ins run out.
        format!("--cookie-expire={SIGN_IN_LASTS_HOURS}h"),
        format!("--cookie-refresh={CHECK_EVERY}"),
        "--code-challenge-method=S256".into(),
        "--email-domain=*".into(),
        "--reverse-proxy=true".into(),
        "--set-xauthrequest=true".into(),
        "--skip-provider-button=true".into(),
        // Caddy only asks it; it serves nothing itself.
        "--upstream=static://202".into(),
    ];
    // Signing out ends at the provider: the gate sends people only to
    // addresses it is told of.
    if let Some(host) = provider.end_session.as_deref().and_then(host_of) {
        if host != domain && !host.ends_with(&format!(".{domain}")) {
            arguments.push(format!("--whitelist-domain={host}"));
        }
    }
    arguments
}

/// What the running gate must match: its settings and secrets.
fn spec(provider: &Provider, domain: &str, gate: Gate) -> String {
    let mut hash = Sha256::new();
    hash.update(IMAGE);
    for argument in arguments(provider, domain, gate) {
        hash.update(argument);
        hash.update([0]);
    }
    hash.update(&provider.client_secret);
    hash.update([0]);
    hash.update(&provider.cookie_secret);
    format!("{:x}", hash.finalize())
}

/// Runs a gate for each domain with a connected provider, recreating one
/// whose provider changed, and removes the gates no domain needs any more.
pub fn sync(gates: &[(Gate, &Provider, &str)]) -> Result<(), String> {
    let wanted: Vec<String> = gates.iter().map(|(gate, ..)| gate.container()).collect();
    for container in gate_containers() {
        if !wanted.contains(&container) {
            remove(&container);
        }
    }
    let mut failed = Vec::new();
    for (gate, provider, domain) in gates {
        if let Err(error) = sync_gate(*gate, provider, domain) {
            failed.push(error);
        }
    }
    match failed.is_empty() {
        true => Ok(()),
        false => Err(failed.join("; ")),
    }
}

fn sync_gate(gate: Gate, provider: &Provider, domain: &str) -> Result<(), String> {
    let container = gate.container();
    let wanted = spec(provider, domain, gate);
    if running_spec(&container).as_deref() == Some(wanted.as_str()) {
        return Ok(());
    }
    remove(&container);
    let output = docker::docker_command()
        .args([
            "run",
            "-d",
            "--name",
            &container,
            "--restart",
            "unless-stopped",
        ])
        .args([
            "--network",
            "host",
            "--log-opt",
            "max-size=10m",
            "--log-opt",
            "max-file=3",
        ])
        .args(["--label", &format!("{SPEC_LABEL}={wanted}")])
        // Named only: the values come from this command's environment, so
        // they are in no command line.
        .args([
            "-e",
            "OAUTH2_PROXY_CLIENT_SECRET",
            "-e",
            "OAUTH2_PROXY_COOKIE_SECRET",
        ])
        .env("OAUTH2_PROXY_CLIENT_SECRET", &provider.client_secret)
        .env("OAUTH2_PROXY_COOKIE_SECRET", &provider.cookie_secret)
        .arg(IMAGE)
        .args(arguments(provider, domain, gate))
        .output()
        .map_err(|e| format!("start company sign-in for {domain}: {e}"))?;
    if !output.status.success() {
        return Err(format!(
            "start company sign-in for {domain}: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    Ok(())
}

/// Every gate container there is, by the label each is started with.
fn gate_containers() -> Vec<String> {
    docker::docker_command()
        .args([
            "ps",
            "-a",
            "--filter",
            &format!("label={SPEC_LABEL}"),
            "--format",
            "{{.Names}}",
        ])
        .output()
        .ok()
        .filter(|output| output.status.success())
        .map(|output| {
            String::from_utf8_lossy(&output.stdout)
                .lines()
                .map(str::trim)
                .filter(|name| !name.is_empty())
                .map(String::from)
                .collect()
        })
        .unwrap_or_default()
}

/// A gate's spec, `None` when there is no such gate. One that Docker is
/// restarting counts: it waits for a provider that does not answer yet, and
/// recreating it would not help.
fn running_spec(container: &str) -> Option<String> {
    let output = docker::docker_command()
        .args([
            "inspect",
            "-f",
            &format!("{{{{.State.Status}}}} {{{{index .Config.Labels \"{SPEC_LABEL}\"}}}}"),
            container,
        ])
        .output()
        .ok()?;
    let text = String::from_utf8_lossy(&output.stdout);
    let (status, spec) = text.trim().split_once(' ')?;
    (output.status.success() && matches!(status, "running" | "restarting"))
        .then(|| spec.to_string())
}

fn remove(container: &str) {
    let _ = docker::docker_command()
        .args(["rm", "-f", container])
        .output();
}

/// Caddy's route for a domain's sign-in address: its gate's own pages, where
/// the provider sends people back, and `/sign-out`, which ends both sessions.
pub fn caddy_route(domain: &str, provider: &Provider, gate: Gate) -> Value {
    json!({
        "match": [{"host": [format!("{NAME}.{domain}")]}],
        "handle": [{
            "handler": "subroute",
            "routes": [
                {
                    "match": [{"path": ["/sign-out"]}],
                    "handle": [{
                        "handler": "static_response",
                        "status_code": 302,
                        "headers": {
                            "Location": [gate_sign_out(provider)],
                            "Cache-Control": ["no-store"],
                        },
                    }],
                },
                {"handle": [{
                    "handler": "reverse_proxy",
                    "upstreams": [{"dial": format!("127.0.0.1:{}", gate.port())}],
                }]},
            ],
        }],
        "terminal": true,
    })
}

/// Removes the identity headers from a request, so only the gate sets them.
pub fn strip_identity() -> Value {
    json!({
        "handler": "headers",
        "request": {"delete": IDENTITY.iter().map(|(_, ours)| *ours).collect::<Vec<_>>()},
    })
}

/// Caddy's check of a request to a company app: the gate answers 2xx with
/// who signed in, which is passed to the app; 401 for no one, sent to sign
/// in; 403 for someone outside the app's groups. Each domain asks its own
/// gate, whose cookie is the domain's.
pub fn caddy_gate(domain: &str, gate: Gate, groups: &[String], forbidden: Value) -> Value {
    let mut uri = "/oauth2/auth".to_string();
    if !groups.is_empty() {
        uri.push_str("?allowed_groups=");
        uri.push_str(&groups.join(","));
    }
    let identity: Vec<Value> = IDENTITY
        .iter()
        .map(|(theirs, ours)| {
            let value = format!("{{http.reverse_proxy.header.{theirs}}}");
            json!({
                "match": [{"not": [{"vars": {value.clone(): [""]}}]}],
                "handle": [{"handler": "headers", "request": {"set": {*ours: [value]}}}],
            })
        })
        .collect();
    json!({
        "handler": "reverse_proxy",
        "upstreams": [{"dial": format!("127.0.0.1:{}", gate.port())}],
        "rewrite": {"method": "GET", "uri": uri},
        "headers": {"request": {"set": {
            "X-Forwarded-Method": ["{http.request.method}"],
            "X-Forwarded-Uri": ["{http.request.uri}"],
        }}},
        "handle_response": [
            {"match": {"status_code": [2]}, "routes": identity},
            {"match": {"status_code": [401]}, "routes": [{"handle": [{
                "handler": "static_response",
                "status_code": 302,
                "headers": {"Location": [format!(
                    "https://{NAME}.{domain}/oauth2/start?rd={{http.request.scheme}}://{{http.request.host}}{{http.request.uri}}"
                )]},
            }]}]},
            {"match": {"status_code": [403]}, "routes": [{"handle": [forbidden]}]},
        ],
    })
}

/// A group name as an app lists it: what providers put in tokens, without
/// the separators the gate's query uses.
pub fn valid_group(group: &str) -> Result<String, String> {
    let group = group.trim();
    if group.is_empty() || group.len() > 200 || group.contains([',', '&', '?', '#', '\n']) {
        return Err(format!("{group} is not a group name"));
    }
    Ok(group.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn provider() -> Provider {
        Provider {
            issuer: "https://login.example.com".into(),
            client_id: "commitarium".into(),
            client_secret: "secret".into(),
            scopes: None,
            groups_claim: None,
            cookie_secret: "c".repeat(32),
            end_session: Some("https://login.example.com/logout".into()),
        }
    }

    #[test]
    fn signing_out_ends_the_gate_then_the_provider() {
        let route = caddy_route("example.com", &provider(), Gate::default());
        let sign_out = &route["handle"][0]["routes"][0];
        assert_eq!(sign_out["match"][0]["path"][0], "/sign-out");
        assert_eq!(
            sign_out["handle"][0]["headers"]["Location"][0],
            "/oauth2/sign_out?rd=https%3A%2F%2Flogin.example.com%2Flogout"
        );
        assert_eq!(
            sign_out_url("example.com"),
            "https://sign-in.example.com/sign-out"
        );
        // The provider's own address is allowed as where signing out ends.
        let arguments = arguments(&provider(), "example.org", Gate::default());
        assert!(arguments.contains(&"--whitelist-domain=login.example.com".to_string()));
        let mut ours = provider();
        ours.end_session = Some("https://auth.example.com/application/o/a/end-session/".into());
        assert!(!arguments_of(&ours)
            .iter()
            .any(|a| a.contains("auth.example.com")));
        ours.end_session = None;
        let route = caddy_route("example.com", &ours, Gate::default());
        assert_eq!(
            route["handle"][0]["routes"][0]["handle"][0]["headers"]["Location"][0],
            "/oauth2/sign_out"
        );
    }

    fn arguments_of(provider: &Provider) -> Vec<String> {
        arguments(provider, "example.com", Gate::default())
    }

    #[test]
    fn the_gate_keeps_its_secrets_out_of_its_arguments() {
        let arguments = arguments(&provider(), "example.com", Gate::default()).join(" ");
        assert!(!arguments.contains("secret"));
        assert!(arguments.contains("--redirect-url=https://sign-in.example.com/oauth2/callback"));
        assert!(arguments.contains("--cookie-domain=.example.com"));
        assert!(arguments.contains("--scope=openid email profile"));
        assert!(arguments.contains("--http-address=127.0.0.1:4180"));
        assert!(arguments.contains("--cookie-expire=8h"));
        assert!(arguments.contains("--cookie-refresh=1h"));
    }

    #[test]
    fn a_changed_secret_or_domain_recreates_the_gate() {
        let first = spec(&provider(), "example.com", Gate::default());
        assert_eq!(first, spec(&provider(), "example.com", Gate::default()));
        let mut changed = provider();
        changed.client_secret = "other".into();
        assert_ne!(first, spec(&changed, "example.com", Gate::default()));
        assert_ne!(first, spec(&provider(), "example.org", Gate::default()));
    }

    #[test]
    fn the_gate_checks_groups_and_passes_who_signed_in() {
        let gate = caddy_gate(
            "example.com",
            Gate::default(),
            &["sales".into(), "Team A".into()],
            json!({}),
        );
        assert_eq!(
            gate["rewrite"]["uri"],
            "/oauth2/auth?allowed_groups=sales,Team A"
        );
        let signed_in = &gate["handle_response"][0]["routes"];
        assert_eq!(signed_in.as_array().unwrap().len(), IDENTITY.len());
        assert_eq!(
            signed_in[1]["handle"][0]["request"]["set"]["X-Commitarium-Email"][0],
            "{http.reverse_proxy.header.X-Auth-Request-Email}"
        );
        let location =
            &gate["handle_response"][1]["routes"][0]["handle"][0]["headers"]["Location"][0];
        assert!(location
            .as_str()
            .unwrap()
            .starts_with("https://sign-in.example.com/oauth2/start?rd="));
        let anyone = caddy_gate("example.com", Gate::default(), &[], json!({}));
        assert_eq!(anyone["rewrite"]["uri"], "/oauth2/auth");
    }

    #[test]
    fn each_domain_has_its_own_gate_and_the_first_keeps_its_old_one() {
        let first = Gate::default();
        assert_eq!(first.container(), "commitarium-sign-in");
        assert_eq!(first.port(), 4180);
        let second = Gate::free(&[first]).unwrap();
        assert_eq!(second.container(), "commitarium-sign-in-1");
        assert_eq!(second.port(), 4181);
        // A gate given up is taken again by the next domain.
        assert_eq!(Gate::free(&[second]).unwrap(), first);
        let arguments = arguments(&provider(), "example.org", second).join(" ");
        assert!(arguments.contains("--http-address=127.0.0.1:4181"));
        assert!(arguments.contains("--cookie-domain=.example.org"));
        assert!(arguments.contains("--redirect-url=https://sign-in.example.org/oauth2/callback"));
        let check = caddy_gate("example.org", second, &[], json!({}));
        assert_eq!(check["upstreams"][0]["dial"], "127.0.0.1:4181");
        let route = caddy_route("example.org", &provider(), second);
        assert_eq!(
            route["handle"][0]["routes"][1]["handle"][0]["upstreams"][0]["dial"],
            "127.0.0.1:4181"
        );
        assert_ne!(
            spec(&provider(), "example.org", first),
            spec(&provider(), "example.org", second)
        );
    }

    #[test]
    fn group_names_and_issuers_are_checked() {
        assert_eq!(valid_group(" sales ").unwrap(), "sales");
        assert!(valid_group("a,b").is_err());
        assert!(valid_group("").is_err());
        let asked = Connect {
            issuer: "http://login.example.com".into(),
            client_id: "id".into(),
            client_secret: "secret".into(),
            scopes: None,
            groups_claim: None,
            domain: None,
        };
        assert!(Provider::connect(asked, None)
            .unwrap_err()
            .contains("https://"));
        let secret = new_cookie_secret().unwrap();
        assert_eq!(secret.len(), 32);
    }

    fn container_id() -> String {
        let output = docker::docker_command()
            .args(["inspect", "-f", "{{.Id}}", CONTAINER])
            .output()
            .unwrap();
        String::from_utf8_lossy(&output.stdout).trim().to_string()
    }

    /// Runs the gate on this computer's Docker: started, left alone while
    /// nothing changes, recreated when the provider does, removed with it.
    /// `cargo test -- --ignored the_gate_follows`.
    #[test]
    #[ignore]
    fn the_gate_follows_the_provider() {
        let mut provider = provider();
        let gate = Gate::default();
        sync(&[(gate, &provider, "example.com")]).unwrap();
        let first = container_id();
        assert!(!first.is_empty());
        assert_eq!(
            running_spec(CONTAINER).as_deref(),
            Some(spec(&provider, "example.com", gate).as_str())
        );
        let output = docker::docker_command()
            .args(["inspect", "-f", "{{json .Args}}", CONTAINER])
            .output()
            .unwrap();
        let arguments = String::from_utf8_lossy(&output.stdout);
        assert!(!arguments.contains(&provider.client_secret), "{arguments}");
        assert!(!arguments.contains(&provider.cookie_secret), "{arguments}");
        sync(&[(gate, &provider, "example.com")]).unwrap();
        assert_eq!(container_id(), first);
        provider.client_secret = "another secret".into();
        sync(&[(gate, &provider, "example.com")]).unwrap();
        let second = container_id();
        assert!(!second.is_empty() && second != first);
        sync(&[]).unwrap();
        assert!(container_id().is_empty());
    }
}
