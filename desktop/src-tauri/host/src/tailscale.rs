//! Tailscale for homelab mode (HOMELAB_PLAN.md, H1): when the owner chooses
//! that only their devices reach the apps, the server joins their Tailscale
//! network, and the apps page and apps are served on its Tailscale name.
//!
//! The host runs as a service user that cannot install system software, so
//! it runs Tailscale as it runs Caddy: a container on the host's network,
//! with its state in a volume. A server that already runs Tailscale itself
//! keeps it, and Commitarium starts nothing.

use crate::docker;
use serde::Serialize;
use serde_json::Value;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

const CONTAINER: &str = "commitarium-tailscale";
const IMAGE: &str = "tailscale/tailscale:v1.104.2";
const STATE: &str = "commitarium-tailscale-state";
const RUN_DIR: &str = "tailscale";
/// Where a Tailscale the server runs itself keeps its socket.
const SYSTEM_SOCKET_DIR: &str = "/var/run/tailscale";
/// Where the owner turns on HTTPS certificates for their tailnet.
pub const DNS_SETTINGS: &str = "https://login.tailscale.com/admin/dns";

/// Where Tailscale stands, in the steps the owner goes through.
#[derive(Clone, Copy, Debug, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum State {
    /// Starting, or not answering yet.
    Starting,
    /// Waiting for the owner to sign in, through `login_url`.
    NeedsLogin,
    /// Signed in, but the tailnet gives no certificates yet: a switch in
    /// its DNS settings, which only its owner can turn on.
    NeedsHttps,
    Ready,
    /// The server's own Tailscale is signed out: its owner signs in there.
    SystemSignedOut,
}

#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct Status {
    pub state: State,
    /// Where to sign in, while it waits for that.
    pub login_url: Option<String>,
    /// The machine's name on the tailnet, such as `homelab.tail1234.ts.net`.
    pub name: Option<String>,
    /// Its Tailscale IPv4 address, such as `100.101.102.103`.
    pub address: Option<String>,
    /// Whether Commitarium runs it, or the server does.
    pub own: bool,
    pub https_settings: &'static str,
}

impl Status {
    /// The name and address apps are served on, once it can serve them.
    pub fn serving(&self) -> Option<(&str, &str)> {
        (self.state == State::Ready).then_some(())?;
        Some((self.name.as_deref()?, self.address.as_deref()?))
    }
}

#[derive(Clone)]
pub struct Tailscale {
    run_dir: PathBuf,
}

impl Tailscale {
    pub fn new(data_dir: &Path) -> Self {
        Self {
            run_dir: data_dir.join(RUN_DIR),
        }
    }

    /// Whether the server runs Tailscale itself.
    fn system() -> bool {
        Path::new(SYSTEM_SOCKET_DIR)
            .join("tailscaled.sock")
            .exists()
    }

    /// The directory holding the socket Caddy gets certificates through.
    pub fn socket_dir(&self) -> PathBuf {
        if Self::system() && !running() {
            PathBuf::from(SYSTEM_SOCKET_DIR)
        } else {
            self.run_dir.clone()
        }
    }

    /// Starts Tailscale unless it runs: the server's own, or ours.
    pub fn keep_running(&self) -> Result<(), String> {
        if running() || Self::system() {
            return Ok(());
        }
        fs::create_dir_all(&self.run_dir)
            .map_err(|e| format!("create {}: {e}", self.run_dir.display()))?;
        let _ = docker::docker_command()
            .args(["rm", "-f", CONTAINER])
            .output();
        let output = docker::docker_command()
            .args([
                "run",
                "-d",
                "--name",
                CONTAINER,
                "--restart",
                "unless-stopped",
            ])
            .args(["--network", "host", "--device", "/dev/net/tun"])
            .args(["--cap-add", "NET_ADMIN", "--cap-add", "NET_RAW"])
            .args(["--log-opt", "max-size=10m", "--log-opt", "max-file=3"])
            .args(["-v", &format!("{STATE}:/var/lib/tailscale")])
            .args([
                "-v",
                &format!("{}:/var/run/tailscale", self.run_dir.display()),
            ])
            .args(["-e", "TS_STATE_DIR=/var/lib/tailscale"])
            .args(["-e", "TS_SOCKET=/var/run/tailscale/tailscaled.sock"])
            .args(["-e", "TS_USERSPACE=false"])
            // No key: it waits for its owner to sign in through the link.
            .args(["-e", "TS_AUTH_ONCE=true"])
            .arg(IMAGE)
            .output()
            .map_err(|e| format!("start Tailscale: {e}"))?;
        if !output.status.success() {
            return Err(format!(
                "start Tailscale: {}",
                String::from_utf8_lossy(&output.stderr).trim()
            ));
        }
        Ok(())
    }

    /// Stops ours. Its state stays, so turning it on again needs no new
    /// sign-in.
    pub fn stop(&self) {
        let _ = docker::docker_command()
            .args(["rm", "-f", CONTAINER])
            .output();
    }

    pub fn status(&self) -> Status {
        let own = running();
        let answer = if own {
            let mut command = docker::docker_command();
            command.args(["exec", CONTAINER, "tailscale", "status", "--json"]);
            read_json(command)
        } else if Self::system() {
            let mut command = crate::process::command("tailscale");
            command.args(["status", "--json"]);
            read_json(command)
        } else {
            None
        };
        match answer {
            Some(json) => parse_status(&json, own),
            None => Status {
                state: State::Starting,
                login_url: None,
                name: None,
                address: None,
                own,
                https_settings: DNS_SETTINGS,
            },
        }
    }
}

fn running() -> bool {
    docker::docker_command()
        .args(["inspect", "-f", "{{.State.Running}}", CONTAINER])
        .output()
        .is_ok_and(|output| output.status.success() && output.stdout.starts_with(b"true"))
}

/// `tailscale status --json` answers even while signed out, though not
/// always with success, so what it prints is read either way.
fn read_json(mut command: Command) -> Option<Value> {
    let output = command.output().ok()?;
    serde_json::from_slice(&output.stdout).ok()
}

/// Where Tailscale stands, from its status.
pub(crate) fn parse_status(json: &Value, own: bool) -> Status {
    let backend = json["BackendState"].as_str().unwrap_or_default();
    let login_url = json["AuthURL"]
        .as_str()
        .filter(|url| url.starts_with("https://"))
        .map(str::to_string);
    let name = json["Self"]["DNSName"]
        .as_str()
        .map(|name| name.trim_end_matches('.').to_string())
        .filter(|name| !name.is_empty());
    let address = json["Self"]["TailscaleIPs"]
        .as_array()
        .into_iter()
        .flatten()
        .filter_map(Value::as_str)
        .find(|ip| ip.contains('.'))
        .map(str::to_string);
    let https = json["CertDomains"]
        .as_array()
        .is_some_and(|domains| !domains.is_empty());
    let state = match backend {
        "Running" if https && name.is_some() && address.is_some() => State::Ready,
        "Running" => State::NeedsHttps,
        "NeedsLogin" | "NeedsMachineAuth" | "Stopped" if !own => State::SystemSignedOut,
        "NeedsLogin" | "NeedsMachineAuth" | "Stopped" => State::NeedsLogin,
        _ => State::Starting,
    };
    Status {
        state,
        login_url: (state == State::NeedsLogin).then_some(login_url).flatten(),
        name,
        address,
        own,
        https_settings: DNS_SETTINGS,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn its_status_reads_as_the_steps_the_owner_takes() {
        let waiting = json!({
            "BackendState": "NeedsLogin",
            "AuthURL": "https://login.tailscale.com/a/abc123",
            "Self": {"DNSName": "", "TailscaleIPs": null},
        });
        let status = parse_status(&waiting, true);
        assert_eq!(status.state, State::NeedsLogin);
        assert_eq!(
            status.login_url.as_deref(),
            Some("https://login.tailscale.com/a/abc123")
        );
        // The server's own Tailscale is signed in there, not through a link.
        assert_eq!(parse_status(&waiting, false).state, State::SystemSignedOut);

        let signed_in = json!({
            "BackendState": "Running",
            "Self": {
                "DNSName": "homelab.tail1234.ts.net.",
                "TailscaleIPs": ["fd7a:115c:a1e0::1", "100.101.102.103"],
            },
            "CertDomains": null,
        });
        let status = parse_status(&signed_in, true);
        assert_eq!(status.state, State::NeedsHttps);
        assert_eq!(status.login_url, None);
        assert!(status.serving().is_none());

        let mut ready = signed_in.clone();
        ready["CertDomains"] = json!(["homelab.tail1234.ts.net"]);
        let status = parse_status(&ready, true);
        assert_eq!(status.state, State::Ready);
        assert_eq!(
            status.serving(),
            Some(("homelab.tail1234.ts.net", "100.101.102.103"))
        );
        assert_eq!(parse_status(&json!({}), true).state, State::Starting);
    }
}
