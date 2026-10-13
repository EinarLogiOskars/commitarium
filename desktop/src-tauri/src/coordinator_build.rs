//! Development only: noticing that the running coordinator was built before
//! the newest commit to its sources (a `git pull` without rebuilding it), and
//! rebuilding it on request. Releases run published images and skip all of
//! this.

use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

use serde::{Deserialize, Serialize};
use tauri::{AppHandle, Manager, State};

use crate::{cli_updates, docker, phase4};

const SERVICE: &str = "coordinator";
/// What the coordinator image is built from, as docker/coordinator.Dockerfile
/// copies it.
const SOURCES: &[&str] = &[
    "cmd/coordinator",
    "internal",
    "go.mod",
    "go.sum",
    "docker/coordinator.Dockerfile",
];
const RECORD_FILE: &str = "coordinator-build.json";
const HEALTH_TIMEOUT: Duration = Duration::from_secs(120);
const HEALTH_POLL: Duration = Duration::from_secs(1);

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Freshness {
    /// Built after the newest commit to its sources, or a release.
    Fresh,
    /// Built before the newest commit to its sources.
    Stale,
    /// Not running, or git or Docker could not tell.
    Unknown,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum RebuildOutcome {
    Rebuilt,
    /// An agent is running a turn; restarting the coordinator waits for it.
    Busy,
}

/// One rebuild at a time.
#[derive(Default)]
pub(crate) struct CoordinatorBuild {
    rebuilding: AtomicBool,
}

/// The image a rebuild left running and the newest source commit it covers.
/// A rebuild that changed nothing is fully cached and keeps its old creation
/// time, so the image alone would stay "older" than a newer commit.
#[derive(Debug, Deserialize, Serialize)]
struct Record {
    image: String,
    commit: i64,
}

/// Whether the running coordinator is older than its sources.
#[tauri::command]
pub async fn coordinator_freshness(app: AppHandle) -> Result<Freshness, String> {
    if docker::release_mode() {
        return Ok(Freshness::Fresh);
    }
    tauri::async_runtime::spawn_blocking(move || freshness(&app))
        .await
        .map_err(|_| "could not inspect the coordinator".to_string())
}

/// Rebuild the coordinator from the sources and restart it, once no agent is
/// running a turn, then wait until it answers again.
#[tauri::command]
pub async fn rebuild_coordinator(
    app: AppHandle,
    build: State<'_, CoordinatorBuild>,
) -> Result<RebuildOutcome, String> {
    if docker::release_mode() {
        return Err("releases update the coordinator with the app".into());
    }
    if build.rebuilding.swap(true, Ordering::SeqCst) {
        return Err("the coordinator is already being rebuilt".into());
    }
    let result = rebuild(app).await;
    build.rebuilding.store(false, Ordering::SeqCst);
    result
}

async fn rebuild(app: AppHandle) -> Result<RebuildOutcome, String> {
    let rebuilt = tauri::async_runtime::spawn_blocking(move || {
        if !cli_updates::all_workers_idle()? {
            return Ok(RebuildOutcome::Busy);
        }
        let commit = newest_commit();
        docker::compose(&[], &["build", SERVICE])?;
        // Building can take minutes; check again right before the restart.
        if !cli_updates::all_workers_idle()? {
            return Ok(RebuildOutcome::Busy);
        }
        docker::compose(&[], &["up", "-d", SERVICE])?;
        if let (Some(image), Some(commit)) = (running_image().map(|(id, _)| id), commit) {
            save_record(&app, &Record { image, commit });
        }
        Ok::<_, String>(RebuildOutcome::Rebuilt)
    })
    .await
    .map_err(|_| "the coordinator rebuild stopped unexpectedly".to_string())??;
    if rebuilt == RebuildOutcome::Rebuilt {
        wait_until_reachable().await?;
    }
    Ok(rebuilt)
}

fn freshness(app: &AppHandle) -> Freshness {
    let Some((image, created)) = running_image() else {
        return Freshness::Unknown;
    };
    let covered = load_record(app)
        .filter(|record| record.image == image)
        .map(|record| record.commit);
    assess(Some(created), newest_commit(), covered)
}

/// Stale when a source commit is newer than both the image and what the last
/// rebuild of that same image covered.
fn assess(
    image_created: Option<i64>,
    newest_commit: Option<i64>,
    covered: Option<i64>,
) -> Freshness {
    match (image_created, newest_commit) {
        (Some(created), Some(commit)) if commit > created.max(covered.unwrap_or(i64::MIN)) => {
            Freshness::Stale
        }
        (Some(_), Some(_)) => Freshness::Fresh,
        _ => Freshness::Unknown,
    }
}

/// The running coordinator's image ID and its creation time.
fn running_image() -> Option<(String, i64)> {
    let container = docker::compose(&[], &["ps", "--quiet", SERVICE]).ok()?;
    let container = container.lines().map(str::trim).find(|id| !id.is_empty())?;
    if !container.bytes().all(|b| b.is_ascii_hexdigit()) {
        return None;
    }
    let image = docker_output(&["inspect", "--format", "{{.Image}}", container])?;
    if !image
        .bytes()
        .all(|b| b.is_ascii_alphanumeric() || b == b':')
    {
        return None;
    }
    let created = docker_output(&["image", "inspect", "--format", "{{.Created}}", &image])?;
    Some((image, parse_rfc3339(&created)?))
}

fn docker_output(args: &[&str]) -> Option<String> {
    let output = docker::docker_command().args(args).output().ok()?;
    output
        .status
        .success()
        .then(|| String::from_utf8_lossy(&output.stdout).trim().to_string())
}

/// The newest commit time (Unix seconds) touching the coordinator's sources in
/// the repository holding the Compose file. Uncommitted changes are ignored.
fn newest_commit() -> Option<i64> {
    let root = docker::compose_file()
        .ok()?
        .parent()
        .map(Path::to_path_buf)?;
    let output = Command::new("git")
        .arg("-C")
        .arg(&root)
        .args(["log", "-1", "--format=%ct", "--"])
        .args(SOURCES)
        .output()
        .ok()?;
    if !output.status.success() {
        return None;
    }
    String::from_utf8_lossy(&output.stdout).trim().parse().ok()
}

async fn wait_until_reachable() -> Result<(), String> {
    let client = reqwest::Client::builder()
        .timeout(Duration::from_secs(5))
        .build()
        .map_err(|e| e.to_string())?;
    let deadline = Instant::now() + HEALTH_TIMEOUT;
    loop {
        let healthy = client
            .get(phase4::coordinator_url("/health"))
            .send()
            .await
            .is_ok_and(|response| response.status().is_success());
        if healthy {
            return Ok(());
        }
        if Instant::now() >= deadline {
            return Err("the rebuilt coordinator did not come back".into());
        }
        tokio::time::sleep(HEALTH_POLL).await;
    }
}

fn record_path(app: &AppHandle) -> Option<PathBuf> {
    Some(
        app.path()
            .app_data_dir()
            .ok()?
            .join("runtime")
            .join(RECORD_FILE),
    )
}

fn load_record(app: &AppHandle) -> Option<Record> {
    serde_json::from_str(&fs::read_to_string(record_path(app)?).ok()?).ok()
}

fn save_record(app: &AppHandle, record: &Record) {
    let Some(path) = record_path(app) else {
        return;
    };
    if let (Some(dir), Ok(contents)) = (path.parent(), serde_json::to_string(record)) {
        let _ = fs::create_dir_all(dir).and_then(|_| fs::write(&path, contents));
    }
}

/// Unix seconds of an RFC 3339 time such as Docker's `Created`
/// (`2026-10-13T12:34:56.123456789Z` or with a `+02:00` offset).
fn parse_rfc3339(value: &str) -> Option<i64> {
    let value = value.trim();
    let (date, time) = value.split_once(['T', 't', ' '])?;
    let mut date = date.split('-').map(|part| part.parse::<i64>().ok());
    let (year, month, day) = (date.next()??, date.next()??, date.next()??);
    if date.next().is_some() || !(1..=12).contains(&month) || !(1..=31).contains(&day) {
        return None;
    }
    let (clock, offset) = if let Some(clock) = time.strip_suffix(['Z', 'z']) {
        (clock, 0)
    } else {
        let at = time.rfind(['+', '-'])?;
        let (clock, zone) = time.split_at(at);
        let sign = if zone.starts_with('-') { -1 } else { 1 };
        let (hours, minutes) = zone[1..].split_once(':')?;
        (
            clock,
            sign * (hours.parse::<i64>().ok()? * 3600 + minutes.parse::<i64>().ok()? * 60),
        )
    };
    let clock = clock.split('.').next()?;
    let mut clock = clock.split(':').map(|part| part.parse::<i64>().ok());
    let (hour, minute, second) = (clock.next()??, clock.next()??, clock.next()??);
    if clock.next().is_some() {
        return None;
    }
    let days = days_from_civil(year, month, day);
    Some(days * 86_400 + hour * 3600 + minute * 60 + second - offset)
}

/// Days since 1970-01-01 of a proleptic Gregorian date.
fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = year.div_euclid(400);
    let year_of_era = year - era * 400;
    let month_index = (month + 9) % 12;
    let day_of_year = (153 * month_index + 2) / 5 + day - 1;
    let day_of_era = year_of_era * 365 + year_of_era / 4 - year_of_era / 100 + day_of_year;
    era * 146_097 + day_of_era - 719_468
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_docker_and_offset_times() {
        assert_eq!(parse_rfc3339("1970-01-01T00:00:00Z"), Some(0));
        assert_eq!(
            parse_rfc3339("2026-10-13T12:34:56.123456789Z"),
            Some(1_791_894_896)
        );
        assert_eq!(
            parse_rfc3339("2026-10-13T14:34:56+02:00"),
            Some(1_791_894_896)
        );
        assert_eq!(
            parse_rfc3339("2024-02-29T00:00:00-01:30"),
            Some(1_709_170_200)
        );
        assert_eq!(parse_rfc3339("yesterday"), None);
        assert_eq!(parse_rfc3339("2026-13-01T00:00:00Z"), None);
    }

    #[test]
    fn a_commit_after_the_build_is_stale() {
        assert_eq!(assess(Some(100), Some(200), None), Freshness::Stale);
        assert_eq!(assess(Some(200), Some(100), None), Freshness::Fresh);
        assert_eq!(assess(Some(200), Some(200), None), Freshness::Fresh);
    }

    #[test]
    fn a_cached_rebuild_covers_the_commits_it_was_built_from() {
        assert_eq!(assess(Some(100), Some(200), Some(200)), Freshness::Fresh);
        assert_eq!(assess(Some(100), Some(300), Some(200)), Freshness::Stale);
    }

    #[test]
    fn without_git_or_an_image_nothing_is_claimed() {
        assert_eq!(assess(Some(100), None, None), Freshness::Unknown);
        assert_eq!(assess(None, Some(100), None), Freshness::Unknown);
    }
}
