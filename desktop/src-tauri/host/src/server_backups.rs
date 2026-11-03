//! A server's own backups (SERVER_COMPLETE_PLAN.md, S1): kept in its data
//! directory, made each night and on request, the last few kept. They use
//! the same code and format as the app's, so either restores the other's.
//! Kept on the server, they do not survive losing it; the user is told so.

use crate::backup::{self, BackupResult};
use crate::cli_updates::CliUpdater;
use crate::events::Events;
use crate::profiles::ProfileManager;
use crate::runtime::block_on;
use crate::{docker, preview};
use serde::{Deserialize, Serialize};
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

const DIR: &str = "backups";
const PREFIX: &str = "backup-";
/// A week of nights.
const KEEP: usize = 7;
const EVERY: Duration = Duration::from_secs(24 * 60 * 60);
/// How often the nightly backup checks whether one is due, and whether the
/// agents are quiet enough: a backup stops the stack for a while.
const CHECK_EVERY: Duration = Duration::from_secs(15 * 60);
pub const PROGRESS_EVENT: &str = "backup_progress";
/// The host's own files a backup carries: what is published and deployed.
/// Device keys stay out: a backup is no way into a server.
const STATE_FILES: &[&str] = &["publishing.json", "deployments.json"];

/// A backup as the list shows it.
#[derive(Serialize)]
pub struct Kept {
    name: String,
    created_at_unix_seconds: u64,
    app_version: String,
    /// Bytes on disk.
    size: u64,
}

#[derive(Deserialize)]
struct Manifest {
    app_version: String,
    created_at_unix_seconds: u64,
}

#[derive(Clone)]
pub struct ServerBackups {
    dir: PathBuf,
    data_dir: PathBuf,
    /// One backup or restore at a time.
    running: Arc<Mutex<()>>,
    /// Which one is under way, for the app to tell a stopped stack from a
    /// server it cannot reach.
    underway: Arc<Mutex<Option<&'static str>>>,
}

/// What a backup needs of the host to stop and start the stack.
pub struct Stack<'a> {
    pub events: Events,
    pub profiles: &'a ProfileManager,
    pub updater: &'a CliUpdater,
}

impl ServerBackups {
    pub fn new(data_dir: &Path) -> Self {
        Self {
            dir: data_dir.join(DIR),
            data_dir: data_dir.to_path_buf(),
            running: Arc::default(),
            underway: Arc::default(),
        }
    }

    /// "backup" or "restore" while one runs.
    pub fn underway(&self) -> Option<&'static str> {
        self.underway.lock().ok().and_then(|underway| *underway)
    }

    fn mark(&self, what: Option<&'static str>) {
        if let Ok(mut underway) = self.underway.lock() {
            *underway = what;
        }
    }

    /// The backups kept, newest first.
    pub fn list(&self) -> Result<Vec<Kept>, String> {
        let Ok(entries) = fs::read_dir(&self.dir) else {
            return Ok(Vec::new());
        };
        let mut kept: Vec<Kept> = entries
            .flatten()
            .filter_map(|entry| {
                let name = entry.file_name().to_str()?.to_string();
                if !name.starts_with(PREFIX) {
                    return None;
                }
                let manifest: Manifest =
                    serde_json::from_slice(&fs::read(entry.path().join("manifest.json")).ok()?)
                        .ok()?;
                Some(Kept {
                    size: size_of(&entry.path()),
                    name,
                    created_at_unix_seconds: manifest.created_at_unix_seconds,
                    app_version: manifest.app_version,
                })
            })
            .collect();
        kept.sort_by_key(|kept| std::cmp::Reverse(kept.created_at_unix_seconds));
        Ok(kept)
    }

    /// Makes a backup now, and keeps only the newest few.
    pub fn create(&self, stack: &Stack) -> Result<BackupResult, String> {
        let _held = self.running.lock().map_err(|_| "a backup is unavailable")?;
        fs::create_dir_all(&self.dir).map_err(|e| format!("create {}: {e}", self.dir.display()))?;
        let destination = self.dir.join(format!("{PREFIX}{}", now()));
        self.mark(Some("backup"));
        let result = self.with_context(stack, |ctx| backup::create(ctx, &destination));
        self.mark(None);
        let result = result?;
        self.prune();
        Ok(result)
    }

    /// Restores one of the backups kept.
    pub fn restore(&self, stack: &Stack, name: &str) -> Result<BackupResult, String> {
        if !self.list()?.iter().any(|kept| kept.name == name) {
            return Err("there is no such backup".into());
        }
        let _held = self.running.lock().map_err(|_| "a backup is unavailable")?;
        let source = self.dir.join(name);
        self.mark(Some("restore"));
        let result = self.with_context(stack, |ctx| backup::restore(ctx, &source));
        self.mark(None);
        result
    }

    fn with_context<T>(
        &self,
        stack: &Stack,
        work: impl FnOnce(&backup::Context) -> Result<T, String>,
    ) -> Result<T, String> {
        let progress = |step: &str| {
            stack
                .events
                .publish(PROGRESS_EVENT, &serde_json::json!({ "step": step }))
        };
        let start_stack = || {
            block_on(docker::stack_start(
                stack.events.clone(),
                stack.profiles,
                stack.updater,
            ))
            .map(|_| ())
        };
        work(&backup::Context {
            state_dir: self.data_dir.clone(),
            state_files: STATE_FILES,
            progress: &progress,
            start_stack: &start_stack,
        })
    }

    fn prune(&self) {
        let Ok(kept) = self.list() else {
            return;
        };
        for old in kept.iter().skip(KEEP) {
            let _ = fs::remove_dir_all(self.dir.join(&old.name));
        }
    }

    /// Makes a backup each night while the agents are quiet, for as long as
    /// the host runs.
    pub fn keep_nightly(self, make: impl Fn(&ServerBackups) + Send + 'static) {
        thread::spawn(move || loop {
            thread::sleep(CHECK_EVERY);
            let due = self
                .list()
                .ok()
                .and_then(|kept| kept.first().map(|newest| newest.created_at_unix_seconds))
                .is_none_or(|newest| now().saturating_sub(newest) >= EVERY.as_secs());
            if due && !agents_working() {
                make(&self);
            }
        });
    }
}

/// Whether any work order is running: a backup stops the stack, which would
/// cut a turn short.
fn agents_working() -> bool {
    #[derive(Deserialize)]
    struct Attention {
        #[serde(default)]
        running: Vec<serde_json::Value>,
    }
    block_on(async {
        let response = reqwest::Client::new()
            .get(preview::coordinator_url("/api/v1/attention"))
            .send()
            .await
            .ok()?;
        response.json::<Attention>().await.ok()
    })
    // A coordinator that does not answer may be mid-start: wait for it.
    .is_none_or(|attention| !attention.running.is_empty())
}

fn size_of(path: &Path) -> u64 {
    fs::read_dir(path)
        .map(|entries| {
            entries
                .flatten()
                .map(|entry| match entry.metadata() {
                    Ok(meta) if meta.is_dir() => size_of(&entry.path()),
                    Ok(meta) => meta.len(),
                    Err(_) => 0,
                })
                .sum()
        })
        .unwrap_or(0)
}

fn now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|elapsed| elapsed.as_secs())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fake_backup(dir: &Path, name: &str, at: u64) {
        let path = dir.join(DIR).join(name);
        fs::create_dir_all(&path).unwrap();
        fs::write(
            path.join("manifest.json"),
            format!(r#"{{"app_version":"0.10.0","created_at_unix_seconds":{at}}}"#),
        )
        .unwrap();
    }

    #[test]
    fn backups_list_newest_first_and_only_the_newest_are_kept() {
        let data = tempfile::tempdir().unwrap();
        for at in 1..=(KEEP as u64 + 2) {
            fake_backup(data.path(), &format!("{PREFIX}{at}"), at);
        }
        fs::create_dir_all(data.path().join(DIR).join("not-a-backup")).unwrap();
        let backups = ServerBackups::new(data.path());
        let kept = backups.list().unwrap();
        assert_eq!(kept.len(), KEEP + 2);
        assert_eq!(kept[0].created_at_unix_seconds, KEEP as u64 + 2);
        backups.prune();
        let kept = backups.list().unwrap();
        assert_eq!(kept.len(), KEEP);
        assert_eq!(kept.last().unwrap().created_at_unix_seconds, 3);
    }
}
