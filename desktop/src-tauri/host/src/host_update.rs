//! How a headless host updates itself, when the user chooses to from their
//! app. The signature is checked with the same key the desktop's updates use.
//! The old file stays beside the new one, and systemd starts the new one once
//! this one leaves. Its stack then moves to the images of its own version,
//! because a host starts the stack with its own tag.

use base64::{engine::general_purpose::STANDARD as BASE64, Engine};
use minisign_verify::{PublicKey, Signature};
use serde::{Deserialize, Serialize};
use std::fs;
use std::os::unix::fs::PermissionsExt;
use std::time::Duration;

const RELEASES: &str = "https://github.com/EinarLogiOskars/commitarium/releases/latest/download";
/// `plugins.updater.pubkey` in `tauri.conf.json`.
const PUBLIC_KEY: &str = "dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWduIHB1YmxpYyBrZXk6IDFDMDZCQjRDOEEzQzFBREYKUldUZkdqeUtUTHNHSEJCRVZEL2lNa1dreVZXcWdsMzlVaWxqTXkxeWo4UEZSajM5YmpvZDZuV04K";
/// Time for the answer to reach the client before the host leaves.
const LEAVE_AFTER: Duration = Duration::from_secs(1);

#[derive(Debug, Deserialize, PartialEq, Serialize)]
pub struct HostUpdate {
    pub current: String,
    pub latest: String,
    pub available: bool,
}

#[derive(Deserialize)]
struct Manifest {
    version: String,
}

/// A version's numbers, then whether it is a release, then a release
/// candidate's numbers: 0.11.0-rc.1 comes after 0.10.0 and before 0.11.0.
fn numbers(version: &str) -> (Vec<u32>, bool, Vec<u32>) {
    let parts = |text: &str| -> Vec<u32> {
        text.split('.')
            .filter_map(|part| {
                part.trim_start_matches(|c: char| !c.is_ascii_digit())
                    .parse()
                    .ok()
            })
            .collect()
    };
    let version = version.trim_start_matches('v');
    match version.split_once('-') {
        Some((release, candidate)) => (parts(release), false, parts(candidate)),
        None => (parts(version), true, Vec::new()),
    }
}

fn is_newer(latest: &str, current: &str) -> bool {
    numbers(latest) > numbers(current)
}

fn verified(file: &[u8], signature: &str, public_key: &str) -> Result<(), String> {
    let text = |base64: &str| {
        BASE64
            .decode(base64.trim())
            .ok()
            .and_then(|bytes| String::from_utf8(bytes).ok())
            .ok_or_else(|| "the update's signature could not be read".to_string())
    };
    let key = PublicKey::decode(&text(public_key)?)
        .map_err(|_| "the update key could not be read".to_string())?;
    let signature = Signature::decode(&text(signature)?)
        .map_err(|_| "the update's signature could not be read".to_string())?;
    key.verify(file, &signature, false)
        .map_err(|_| "the update's signature did not match".to_string())
}

async fn fetch(client: &reqwest::Client, name: &str) -> Result<reqwest::Response, String> {
    client
        .get(format!("{RELEASES}/{name}"))
        .send()
        .await
        .and_then(reqwest::Response::error_for_status)
        .map_err(|_| "could not reach the update server".to_string())
}

pub async fn check() -> Result<HostUpdate, String> {
    let manifest: Manifest = fetch(&reqwest::Client::new(), "latest.json")
        .await?
        .json()
        .await
        .map_err(|_| "the update information could not be read".to_string())?;
    let current = env!("CARGO_PKG_VERSION").to_string();
    Ok(HostUpdate {
        available: is_newer(&manifest.version, &current),
        current,
        latest: manifest.version,
    })
}

/// Replaces this program and leaves. systemd brings the new one up.
pub async fn install() -> Result<(), String> {
    let client = reqwest::Client::new();
    let name = format!("commitarium-host-linux-{}", std::env::consts::ARCH);
    let file = fetch(&client, &name)
        .await?
        .bytes()
        .await
        .map_err(|_| "the update did not finish downloading".to_string())?;
    let signature = fetch(&client, &format!("{name}.sig"))
        .await?
        .text()
        .await
        .map_err(|_| "the update did not finish downloading".to_string())?;
    verified(&file, &signature, PUBLIC_KEY)?;

    let program = std::env::current_exe().map_err(|e| format!("find this program: {e}"))?;
    let replacement = program.with_extension("new");
    fs::write(&replacement, &file)
        .and_then(|()| fs::set_permissions(&replacement, fs::Permissions::from_mode(0o755)))
        .and_then(|()| fs::rename(&program, program.with_extension("previous")))
        .and_then(|()| fs::rename(&replacement, &program))
        .map_err(|e| format!("replace this program: {e}"))?;
    crate::runtime::spawn(async {
        tokio::time::sleep(LEAVE_AFTER).await;
        std::process::exit(0);
    });
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_a_higher_version_is_an_update() {
        assert!(is_newer("0.9.10", "0.9.3"));
        assert!(is_newer("v1.0.0", "0.9.3"));
        assert!(!is_newer("0.9.3", "0.9.3"));
        assert!(!is_newer("0.9.2", "0.9.3"));
        // A release candidate comes between the release before and its own.
        assert!(is_newer("0.11.0-rc.1", "0.10.0"));
        assert!(is_newer("0.11.0", "0.11.0-rc.1"));
        assert!(is_newer("0.11.0-rc.2", "0.11.0-rc.1"));
        assert!(!is_newer("0.10.0", "0.11.0-rc.1"));
    }

    #[test]
    fn the_key_is_the_one_the_desktop_trusts() {
        let config = include_str!("../../tauri.conf.json");
        assert!(config.contains(PUBLIC_KEY));
        PublicKey::decode(&String::from_utf8(BASE64.decode(PUBLIC_KEY).unwrap()).unwrap()).unwrap();
    }

    #[test]
    fn a_bad_signature_is_refused() {
        assert!(verified(b"file", "not base64!", PUBLIC_KEY).is_err());
        assert!(verified(b"file", &BASE64.encode("nonsense"), PUBLIC_KEY).is_err());
    }
}
