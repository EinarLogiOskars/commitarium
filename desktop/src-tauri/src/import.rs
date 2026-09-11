//! Host-side project import.
//!
//! Turns a folder on the user's disk into a private Forgejo-backed project
//! WITHOUT modifying that folder. A Git repo is bundled from its committed
//! history; a plain folder is snapshotted into a throwaway repo whose git data
//! lives entirely in a temp directory (GIT_DIR/GIT_WORK_TREE), so no `.git` is
//! ever written into the user's folder and their `.gitignore` is still honored.
//! The bundle is uploaded to the coordinator's import endpoint, which copies it
//! into the internal forge. The user's folder is never touched, and nothing is
//! pushed to any external remote.

use serde::Serialize;
use serde_json::json;
use std::path::{Path, PathBuf};
use std::process::Command;
use tauri::{AppHandle, Manager};

const COORDINATOR_BASE: &str = "http://127.0.0.1:8080";
const IMPORT_AUTHOR: &str = "Commitarium Import";
const IMPORT_EMAIL: &str = "import@commitarium.local";
// Rough estimate only (the real import honors .gitignore); skip obvious noise.
const SKIP_DIRS: &[&str] = &[".git", "node_modules", "target", "dist", ".venv", ".next"];

/// What we learned about a picked folder, to drive the import dialog.
#[derive(Serialize)]
pub struct FolderInfo {
    path: String,
    suggested_name: String,
    is_git_repo: bool,
    has_commits: bool,
    dirty: bool,
    current_branch: Option<String>,
    /// Estimated file count for a plain (non-repo) folder.
    estimated_files: Option<u64>,
    /// Estimated total bytes for a plain folder.
    estimated_bytes: Option<u64>,
}

fn git(dir: &Path, args: &[&str]) -> Result<std::process::Output, String> {
    Command::new("git")
        .arg("-C")
        .arg(dir)
        .args(args)
        .output()
        .map_err(|e| format!("failed to run git: {e}"))
}

fn git_ok(dir: &Path, args: &[&str]) -> bool {
    git(dir, args).map(|o| o.status.success()).unwrap_or(false)
}

fn git_line(dir: &Path, args: &[&str]) -> Option<String> {
    let out = git(dir, args).ok()?;
    if !out.status.success() {
        return None;
    }
    let s = String::from_utf8_lossy(&out.stdout).trim().to_string();
    if s.is_empty() {
        None
    } else {
        Some(s)
    }
}

pub(crate) fn is_repo_root(dir: &Path) -> bool {
    // Inside a work tree, and this folder is that work tree's top level (not a
    // parent repo the folder happens to sit under).
    if !git_ok(dir, &["rev-parse", "--is-inside-work-tree"]) {
        return false;
    }
    match git_line(dir, &["rev-parse", "--show-toplevel"]) {
        Some(top) => Path::new(&top) == dir,
        None => false,
    }
}

fn estimate_plain_folder(dir: &Path) -> (u64, u64) {
    fn walk(dir: &Path, files: &mut u64, bytes: &mut u64) {
        let entries = match std::fs::read_dir(dir) {
            Ok(e) => e,
            Err(_) => return,
        };
        for entry in entries.flatten() {
            let name = entry.file_name();
            let name = name.to_string_lossy();
            let path = entry.path();
            if path.is_dir() {
                if SKIP_DIRS.contains(&name.as_ref()) {
                    continue;
                }
                walk(&path, files, bytes);
            } else if let Ok(meta) = entry.metadata() {
                *files += 1;
                *bytes += meta.len();
            }
        }
    }
    let mut files = 0;
    let mut bytes = 0;
    walk(dir, &mut files, &mut bytes);
    (files, bytes)
}

/// Inspect a picked folder to decide how it can be imported.
#[tauri::command]
pub fn inspect_folder(path: String) -> Result<FolderInfo, String> {
    let dir = PathBuf::from(&path);
    if !dir.is_dir() {
        return Err("that path is not a folder".to_string());
    }
    let suggested_name = dir
        .file_name()
        .map(|n| n.to_string_lossy().to_string())
        .unwrap_or_else(|| "Imported project".to_string());

    let is_git_repo = is_repo_root(&dir);
    if is_git_repo {
        let has_commits = git_ok(&dir, &["rev-parse", "HEAD"]);
        let dirty = git(&dir, &["status", "--porcelain"])
            .map(|o| !o.stdout.is_empty())
            .unwrap_or(false);
        let current_branch = git_line(&dir, &["rev-parse", "--abbrev-ref", "HEAD"]);
        Ok(FolderInfo {
            path,
            suggested_name,
            is_git_repo: true,
            has_commits,
            dirty,
            current_branch,
            estimated_files: None,
            estimated_bytes: None,
        })
    } else {
        let (files, bytes) = estimate_plain_folder(&dir);
        Ok(FolderInfo {
            path,
            suggested_name,
            is_git_repo: false,
            has_commits: false,
            dirty: false,
            current_branch: None,
            estimated_files: Some(files),
            estimated_bytes: Some(bytes),
        })
    }
}

/// Produce a Git bundle for `dir` at `bundle_path`, choosing repo vs snapshot.
/// Returns the default branch the bundle should declare.
fn build_bundle(dir: &Path, default_branch: &str, bundle_path: &Path) -> Result<(), String> {
    let bundle_str = bundle_path.to_str().ok_or("bundle path not UTF-8")?;

    if is_repo_root(dir) {
        if !git_ok(dir, &["rev-parse", "HEAD"]) {
            return Err("this Git repository has no commits yet — make one first".into());
        }
        let dirty = git(dir, &["status", "--porcelain"])
            .map(|o| !o.stdout.is_empty())
            .unwrap_or(false);
        if dirty {
            return Err(
                "this repository has uncommitted changes — commit them first so the import captures your work".into(),
            );
        }
        let out = git(dir, &["bundle", "create", bundle_str, "--all"])?;
        if !out.status.success() {
            return Err(format!(
                "git bundle failed: {}",
                String::from_utf8_lossy(&out.stderr).trim()
            ));
        }
        return Ok(());
    }

    // Plain folder: snapshot into a temp repo without touching the folder.
    let git_dir = tempfile::tempdir().map_err(|e| format!("temp dir: {e}"))?;
    let run = |args: &[&str]| -> Result<std::process::Output, String> {
        Command::new("git")
            .env("GIT_DIR", git_dir.path())
            .env("GIT_WORK_TREE", dir)
            .args(args)
            .output()
            .map_err(|e| format!("failed to run git: {e}"))
    };
    let check = |out: std::process::Output, what: &str| -> Result<(), String> {
        if out.status.success() {
            Ok(())
        } else {
            Err(format!("{what}: {}", String::from_utf8_lossy(&out.stderr).trim()))
        }
    };

    check(
        run(&["-c", &format!("init.defaultBranch={default_branch}"), "init"])?,
        "git init",
    )?;
    check(run(&["add", "-A"])?, "git add")?;
    check(
        run(&[
            "-c",
            &format!("user.name={IMPORT_AUTHOR}"),
            "-c",
            &format!("user.email={IMPORT_EMAIL}"),
            "commit",
            "--allow-empty",
            "-m",
            "Import project into Commitarium",
        ])?,
        "git commit",
    )?;
    check(run(&["bundle", "create", bundle_str, "--all"])?, "git bundle")?;
    Ok(())
}

fn slugify(input: &str) -> String {
    let mut out = String::new();
    for ch in input.chars() {
        if ch.is_ascii_alphanumeric() {
            out.push(ch.to_ascii_lowercase());
        } else if !out.ends_with('-') {
            out.push('-');
        }
    }
    out.trim_matches('-').to_string()
}

/// A fresh, safe import id per attempt. The coordinator's retry-by-id contract
/// wants byte-identical bundle bytes on retry, but `git bundle` output is not
/// reproducible across runs (and plain-folder snapshots get a new commit each
/// time), so reusing an id would only trip the mismatch path. A unique id makes
/// each import its own operation — re-importing a folder yields a new project.
fn import_id(name: &str, abs_path: &Path) -> String {
    use std::hash::{Hash, Hasher};
    use std::time::{SystemTime, UNIX_EPOCH};
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos())
        .unwrap_or(0);
    let mut h = std::collections::hash_map::DefaultHasher::new();
    abs_path.hash(&mut h);
    nanos.hash(&mut h);
    let slug = slugify(name);
    let slug = if slug.is_empty() { "project".into() } else { slug };
    format!("{slug}-{:016x}", h.finish())
}

/// Import a folder as a new private project. Returns the created project JSON.
#[tauri::command]
pub async fn import_project(
    app: AppHandle,
    path: String,
    name: String,
    default_branch: String,
    recovery_policy: String,
) -> Result<serde_json::Value, String> {
    let dir = PathBuf::from(&path);
    if !dir.is_dir() {
        return Err("that path is not a folder".to_string());
    }
    let abs = std::fs::canonicalize(&dir).unwrap_or(dir.clone());

    let bundle_dir = tempfile::tempdir().map_err(|e| format!("temp dir: {e}"))?;
    let bundle_path = bundle_dir.path().join("project.bundle");
    build_bundle(&dir, &default_branch, &bundle_path)?;

    let bytes = std::fs::read(&bundle_path).map_err(|e| format!("read bundle: {e}"))?;
    let metadata = json!({
        "name": name,
        "default_branch": default_branch,
        "recovery_policy": recovery_policy,
    })
    .to_string();

    let id = import_id(&name, &abs);
    let form = reqwest::multipart::Form::new().text("metadata", metadata).part(
        "bundle",
        reqwest::multipart::Part::bytes(bytes).file_name("project.bundle"),
    );

    let client = reqwest::Client::new();
    let resp = client
        .put(format!("{COORDINATOR_BASE}/api/v1/project-imports/{id}"))
        .multipart(form)
        .send()
        .await
        .map_err(|e| format!("upload to coordinator failed: {e}"))?;

    let status = resp.status();
    let body = resp.text().await.unwrap_or_default();
    if !status.is_success() {
        // Surface the coordinator's error envelope when present.
        if let Ok(v) = serde_json::from_str::<serde_json::Value>(&body) {
            if let Some(msg) = v.pointer("/error/message").and_then(|m| m.as_str()) {
                return Err(msg.to_string());
            }
        }
        return Err(format!("import failed (HTTP {}): {}", status.as_u16(), body));
    }

    let project: serde_json::Value =
        serde_json::from_str(&body).map_err(|e| format!("parse project: {e}"))?;

    // Retain the source path in trusted local state for the future handoff.
    if let Some(pid) = project.get("id").and_then(|v| v.as_str()) {
        if let Err(e) = record_source(&app, pid, abs.to_string_lossy().as_ref()) {
            eprintln!("warning: could not record project source path: {e}");
        }
    }

    Ok(project)
}

fn sources_path(app: &AppHandle) -> Result<PathBuf, String> {
    let dir = app
        .path()
        .app_data_dir()
        .map_err(|e| format!("app data dir: {e}"))?;
    std::fs::create_dir_all(&dir).map_err(|e| format!("create app data dir: {e}"))?;
    Ok(dir.join("project-sources.json"))
}

fn record_source(app: &AppHandle, project_id: &str, source_path: &str) -> Result<(), String> {
    let path = sources_path(app)?;
    let mut map: serde_json::Map<String, serde_json::Value> = match std::fs::read_to_string(&path) {
        Ok(s) => serde_json::from_str(&s).unwrap_or_default(),
        Err(_) => serde_json::Map::new(),
    };
    map.insert(project_id.to_string(), json!(source_path));
    std::fs::write(
        &path,
        serde_json::to_string_pretty(&serde_json::Value::Object(map)).unwrap_or_default(),
    )
    .map_err(|e| format!("write sources: {e}"))
}

/// Look up the on-disk source path recorded for a project, if any.
#[tauri::command]
pub fn get_project_source(app: AppHandle, project_id: String) -> Result<Option<String>, String> {
    let path = sources_path(&app)?;
    let map: serde_json::Map<String, serde_json::Value> = match std::fs::read_to_string(&path) {
        Ok(s) => serde_json::from_str(&s).unwrap_or_default(),
        Err(_) => return Ok(None),
    };
    Ok(map
        .get(&project_id)
        .and_then(|v| v.as_str())
        .map(|s| s.to_string()))
}
