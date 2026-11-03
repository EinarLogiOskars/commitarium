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

/// The address the company's provider sends people back to: one per server,
/// registered once with the provider.
pub const NAME: &str = "sign-in";
const PORT: u16 = 4180;
const CONTAINER: &str = "commitarium-sign-in";
const IMAGE: &str = "quay.io/oauth2-proxy/oauth2-proxy:v7.12.0";
/// Which provider settings the running gate was started with.
const SPEC_LABEL: &str = "commitarium.sign-in.spec";
const DEFAULT_SCOPES: &str = "openid email profile";
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
        let issuer = check_issuer(&issuer)?;
        Ok(Self {
            issuer,
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

fn check_issuer(issuer: &str) -> Result<String, String> {
    #[derive(Deserialize)]
    struct Discovery {
        issuer: String,
        authorization_endpoint: String,
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
    Ok(discovery.issuer)
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
pub(crate) fn arguments(provider: &Provider, domain: &str) -> Vec<String> {
    vec![
        "--provider=oidc".into(),
        format!("--oidc-issuer-url={}", provider.issuer),
        format!("--client-id={}", provider.client_id),
        format!("--scope={}", provider.scopes()),
        format!("--oidc-groups-claim={}", provider.groups_claim()),
        format!("--redirect-url={}", redirect_url(domain)),
        format!("--cookie-domain=.{domain}"),
        format!("--whitelist-domain=.{domain}"),
        format!("--http-address=127.0.0.1:{PORT}"),
        "--cookie-secure=true".into(),
        "--cookie-name=_commitarium_sign_in".into(),
        "--code-challenge-method=S256".into(),
        "--email-domain=*".into(),
        "--reverse-proxy=true".into(),
        "--set-xauthrequest=true".into(),
        "--skip-provider-button=true".into(),
        // Caddy only asks it; it serves nothing itself.
        "--upstream=static://202".into(),
    ]
}

/// What the running gate must match: its settings and secrets.
fn spec(provider: &Provider, domain: &str) -> String {
    let mut hash = Sha256::new();
    hash.update(IMAGE);
    for argument in arguments(provider, domain) {
        hash.update(argument);
        hash.update([0]);
    }
    hash.update(&provider.client_secret);
    hash.update([0]);
    hash.update(&provider.cookie_secret);
    format!("{:x}", hash.finalize())
}

/// Runs the gate for the connected provider, recreating it when the
/// provider changed, or removes it when there is none.
pub fn sync(provider: Option<&Provider>, domain: &str) -> Result<(), String> {
    let running = running_spec();
    let Some(provider) = provider else {
        if running.is_some() {
            remove();
        }
        return Ok(());
    };
    let wanted = spec(provider, domain);
    if running.as_deref() == Some(wanted.as_str()) {
        return Ok(());
    }
    remove();
    let output = docker::docker_command()
        .args([
            "run",
            "-d",
            "--name",
            CONTAINER,
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
        .args(arguments(provider, domain))
        .output()
        .map_err(|e| format!("start company sign-in: {e}"))?;
    if !output.status.success() {
        return Err(format!(
            "start company sign-in: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    Ok(())
}

/// The running gate's spec, `None` when it is not running.
fn running_spec() -> Option<String> {
    let output = docker::docker_command()
        .args([
            "inspect",
            "-f",
            &format!("{{{{.State.Running}}}} {{{{index .Config.Labels \"{SPEC_LABEL}\"}}}}"),
            CONTAINER,
        ])
        .output()
        .ok()?;
    let text = String::from_utf8_lossy(&output.stdout);
    let (running, spec) = text.trim().split_once(' ')?;
    (output.status.success() && running == "true").then(|| spec.to_string())
}

fn remove() {
    let _ = docker::docker_command()
        .args(["rm", "-f", CONTAINER])
        .output();
}

/// Caddy's route for the sign-in address: the gate's own pages, where the
/// provider sends people back.
pub fn caddy_route(domain: &str) -> Value {
    json!({
        "match": [{"host": [format!("{NAME}.{domain}")]}],
        "handle": [{
            "handler": "reverse_proxy",
            "upstreams": [{"dial": format!("127.0.0.1:{PORT}")}],
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
/// in; 403 for someone outside the app's groups.
pub fn caddy_gate(domain: &str, groups: &[String], forbidden: Value) -> Value {
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
        "upstreams": [{"dial": format!("127.0.0.1:{PORT}")}],
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
        }
    }

    #[test]
    fn the_gate_keeps_its_secrets_out_of_its_arguments() {
        let arguments = arguments(&provider(), "example.com").join(" ");
        assert!(!arguments.contains("secret"));
        assert!(arguments.contains("--redirect-url=https://sign-in.example.com/oauth2/callback"));
        assert!(arguments.contains("--cookie-domain=.example.com"));
        assert!(arguments.contains("--scope=openid email profile"));
        assert!(arguments.contains("--http-address=127.0.0.1:4180"));
    }

    #[test]
    fn a_changed_secret_or_domain_recreates_the_gate() {
        let first = spec(&provider(), "example.com");
        assert_eq!(first, spec(&provider(), "example.com"));
        let mut changed = provider();
        changed.client_secret = "other".into();
        assert_ne!(first, spec(&changed, "example.com"));
        assert_ne!(first, spec(&provider(), "example.org"));
    }

    #[test]
    fn the_gate_checks_groups_and_passes_who_signed_in() {
        let gate = caddy_gate("example.com", &["sales".into(), "Team A".into()], json!({}));
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
        let anyone = caddy_gate("example.com", &[], json!({}));
        assert_eq!(anyone["rewrite"]["uri"], "/oauth2/auth");
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
        };
        assert!(Provider::connect(asked, None)
            .unwrap_err()
            .contains("https://"));
        let secret = new_cookie_secret().unwrap();
        assert_eq!(secret.len(), 32);
    }
}
