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

use serde::{Deserialize, Serialize};
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::Command;
use tauri::{AppHandle, Manager};

const COORDINATOR_BASE: &str = "http://127.0.0.1:8080";
const IMPORT_AUTHOR: &str = "Commitarium Import";
const IMPORT_EMAIL: &str = "import@commitarium.local";
// Rough estimate only (the real import honors .gitignore); skip obvious noise.
const SKIP_DIRS: &[&str] = &[".git", "node_modules", "target", "dist", ".venv", ".next"];
static PROJECT_SOURCES_LOCK: std::sync::Mutex<()> = std::sync::Mutex::new(());

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

/// Optional project defaults forwarded with a host-side import. These mirror
/// the coordinator's project settings payload; validation remains centralized
/// in the coordinator so imports return the same errors as project creation.
#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ImportAgentProviders {
    lead: String,
    reviewer: String,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ImportAgentModels {
    lead: String,
    reviewer: String,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
pub struct ImportDialogueLimits {
    planning_rounds: i64,
    implementation_review_rounds: i64,
}

#[derive(Debug, Serialize)]
struct ImportMetadata {
    name: String,
    default_branch: String,
    recovery_policy: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    agent_providers: Option<ImportAgentProviders>,
    #[serde(skip_serializing_if = "Option::is_none")]
    agent_models: Option<ImportAgentModels>,
    #[serde(skip_serializing_if = "Option::is_none")]
    autonomy_policy: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    merge_policy: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    dialogue_limits: Option<ImportDialogueLimits>,
}

fn import_metadata_json(metadata: ImportMetadata) -> Result<String, String> {
    serde_json::to_string(&metadata).map_err(|e| format!("encode import metadata: {e}"))
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
        Some(top) => {
            let expected = std::fs::canonicalize(dir).unwrap_or_else(|_| dir.to_path_buf());
            let actual = std::fs::canonicalize(&top).unwrap_or_else(|_| PathBuf::from(top));
            actual == expected
        }
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
fn build_bundle(dir: &Path, default_branch: &str, bundle_path: &Path) -> Result<String, String> {
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
        return git_line(dir, &["rev-parse", &format!("{default_branch}^{{commit}}")])
            .ok_or_else(|| format!("default branch {default_branch} does not exist locally"));
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
            Err(format!(
                "{what}: {}",
                String::from_utf8_lossy(&out.stderr).trim()
            ))
        }
    };

    check(
        run(&[
            "-c",
            &format!("init.defaultBranch={default_branch}"),
            "init",
        ])?,
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
    check(
        run(&["bundle", "create", bundle_str, "--all"])?,
        "git bundle",
    )?;
    let head = run(&["rev-parse", "HEAD"])?;
    if !head.status.success() {
        return Err(format!(
            "resolve plain-folder import commit: {}",
            String::from_utf8_lossy(&head.stderr).trim()
        ));
    }
    let commit = String::from_utf8_lossy(&head.stdout).trim().to_string();
    Ok(commit)
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
    let slug = if slug.is_empty() {
        "project".into()
    } else {
        slug
    };
    format!("{slug}-{:016x}", h.finish())
}

/// Import a folder as a new private project. Returns the created project JSON.
#[tauri::command]
// The arguments intentionally mirror the documented Tauri IPC contract.
#[allow(clippy::too_many_arguments)]
pub async fn import_project(
    app: AppHandle,
    path: String,
    name: String,
    default_branch: String,
    recovery_policy: String,
    agent_providers: Option<ImportAgentProviders>,
    agent_models: Option<ImportAgentModels>,
    autonomy_policy: Option<String>,
    merge_policy: Option<String>,
    dialogue_limits: Option<ImportDialogueLimits>,
) -> Result<serde_json::Value, String> {
    let dir = PathBuf::from(&path);
    if !dir.is_dir() {
        return Err("that path is not a folder".to_string());
    }
    let abs = std::fs::canonicalize(&dir).unwrap_or(dir.clone());

    let bundle_dir = tempfile::tempdir().map_err(|e| format!("temp dir: {e}"))?;
    let bundle_path = bundle_dir.path().join("project.bundle");
    let import_commit_id = build_bundle(&dir, &default_branch, &bundle_path)?;
    let source_kind = if is_repo_root(&dir) {
        "git"
    } else {
        "plain_folder"
    };

    let bytes = std::fs::read(&bundle_path).map_err(|e| format!("read bundle: {e}"))?;
    let metadata = import_metadata_json(ImportMetadata {
        name: name.clone(),
        default_branch,
        recovery_policy,
        agent_providers,
        agent_models,
        autonomy_policy,
        merge_policy,
        dialogue_limits,
    })?;

    let id = import_id(&name, &abs);
    let form = reqwest::multipart::Form::new()
        .text("metadata", metadata)
        .part(
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
        return Err(format!(
            "import failed (HTTP {}): {}",
            status.as_u16(),
            body
        ));
    }

    let project: serde_json::Value =
        serde_json::from_str(&body).map_err(|e| format!("parse project: {e}"))?;

    // Retain the source path in trusted local state for the future handoff.
    if let Some(pid) = project.get("id").and_then(|v| v.as_str()) {
        if let Err(e) = record_source(
            &app,
            pid,
            ProjectSource {
                path: abs.to_string_lossy().into_owned(),
                source_type: source_kind.to_string(),
                import_commit_id: Some(import_commit_id),
                created_by_commitarium: false,
            },
        ) {
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

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
pub(crate) struct ProjectSource {
    pub(crate) path: String,
    pub(crate) source_type: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub(crate) import_commit_id: Option<String>,
    #[serde(default)]
    pub(crate) created_by_commitarium: bool,
}

pub(crate) fn record_source(
    app: &AppHandle,
    project_id: &str,
    source: ProjectSource,
) -> Result<(), String> {
    let _guard = PROJECT_SOURCES_LOCK
        .lock()
        .map_err(|_| "project source storage lock is unavailable".to_string())?;
    let path = sources_path(app)?;
    let mut map: serde_json::Map<String, serde_json::Value> = match std::fs::read_to_string(&path) {
        Ok(s) => serde_json::from_str(&s).unwrap_or_default(),
        Err(_) => serde_json::Map::new(),
    };
    map.insert(
        project_id.to_string(),
        serde_json::to_value(source).map_err(|e| format!("encode project source: {e}"))?,
    );
    write_source_map(&path, map)
}

/// Look up the on-disk source path recorded for a project, if any.
#[tauri::command]
pub fn get_project_source(app: AppHandle, project_id: String) -> Result<Option<String>, String> {
    Ok(get_project_source_record(&app, &project_id)?.map(|source| source.path))
}

/// Delete a project through the coordinator, then forget only its trusted
/// host-side source mapping. If the local write fails, retrying the same key
/// replays coordinator success and repairs this final host-only step.
#[tauri::command]
pub async fn delete_project(
    app: AppHandle,
    project_id: String,
    idempotency_key: String,
    force: Option<bool>,
) -> Result<serde_json::Value, String> {
    if project_id.trim().is_empty() || idempotency_key.trim().is_empty() {
        return Err("project ID and Idempotency-Key are required".into());
    }
    let mut url =
        reqwest::Url::parse(COORDINATOR_BASE).map_err(|e| format!("coordinator URL: {e}"))?;
    url.path_segments_mut()
        .map_err(|_| "coordinator URL cannot accept a project path".to_string())?
        .extend(["api", "v1", "projects", project_id.trim()]);
    if force.unwrap_or(false) {
        url.query_pairs_mut().append_pair("force", "true");
    }
    let response = reqwest::Client::new()
        .delete(url)
        .header("Idempotency-Key", idempotency_key.trim())
        .send()
        .await
        .map_err(|e| format!("delete project through coordinator: {e}"))?;
    let status = response.status();
    let body = response.text().await.unwrap_or_default();
    if !status.is_success() {
        if let Ok(value) = serde_json::from_str::<serde_json::Value>(&body) {
            if let (Some(code), Some(message)) = (
                value.pointer("/error/code").and_then(|item| item.as_str()),
                value
                    .pointer("/error/message")
                    .and_then(|item| item.as_str()),
            ) {
                return Err(format!("{message} ({code})"));
            }
        }
        return Err(format!(
            "project deletion failed (HTTP {}): {}",
            status.as_u16(),
            body
        ));
    }
    let result = serde_json::from_str(&body).map_err(|e| format!("parse project deletion: {e}"))?;
    remove_source(&app, project_id.trim())?;
    crate::handoff::project::remove_project_handoff_state(&app, project_id.trim())?;
    crate::git_providers::remove_project_remote_state(&app, project_id.trim())?;
    Ok(result)
}

fn remove_source(app: &AppHandle, project_id: &str) -> Result<(), String> {
    let _guard = PROJECT_SOURCES_LOCK
        .lock()
        .map_err(|_| "project source storage lock is unavailable".to_string())?;
    remove_source_at(&sources_path(app)?, project_id)
}

fn remove_source_at(path: &Path, project_id: &str) -> Result<(), String> {
    let contents = match std::fs::read_to_string(path) {
        Ok(contents) => contents,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(error) => return Err(format!("read sources: {error}")),
    };
    let mut map: serde_json::Map<String, serde_json::Value> =
        serde_json::from_str(&contents).map_err(|e| format!("parse sources: {e}"))?;
    if map.remove(project_id).is_none() {
        return Ok(());
    }
    write_source_map(path, map)
}

fn write_source_map(
    path: &Path,
    map: serde_json::Map<String, serde_json::Value>,
) -> Result<(), String> {
    let encoded = serde_json::to_vec_pretty(&serde_json::Value::Object(map))
        .map_err(|e| format!("encode sources: {e}"))?;
    let parent = path
        .parent()
        .ok_or_else(|| "project source path has no parent".to_string())?;
    let mut temporary = tempfile::NamedTempFile::new_in(parent)
        .map_err(|e| format!("create temporary sources file: {e}"))?;
    temporary
        .write_all(&encoded)
        .and_then(|_| temporary.as_file().sync_all())
        .map_err(|e| format!("write temporary sources file: {e}"))?;
    temporary
        .persist(path)
        .map_err(|e| format!("replace sources: {}", e.error))?;
    Ok(())
}

pub(crate) fn get_project_source_record(
    app: &AppHandle,
    project_id: &str,
) -> Result<Option<ProjectSource>, String> {
    let _guard = PROJECT_SOURCES_LOCK
        .lock()
        .map_err(|_| "project source storage lock is unavailable".to_string())?;
    let path = sources_path(app)?;
    let map: serde_json::Map<String, serde_json::Value> = match std::fs::read_to_string(&path) {
        Ok(s) => serde_json::from_str(&s).unwrap_or_default(),
        Err(_) => return Ok(None),
    };
    let Some(value) = map.get(project_id) else {
        return Ok(None);
    };
    if let Some(path) = value.as_str() {
        let source_type = if is_repo_root(Path::new(path)) {
            "git"
        } else {
            "plain_folder"
        };
        return Ok(Some(ProjectSource {
            path: path.to_string(),
            source_type: source_type.to_string(),
            import_commit_id: None,
            created_by_commitarium: false,
        }));
    }
    let source: ProjectSource = serde_json::from_value(value.clone())
        .map_err(|e| format!("parse trusted project source: {e}"))?;
    if source.source_type != "git" && source.source_type != "plain_folder" {
        return Err("trusted project source has an unsupported type".into());
    }
    Ok(Some(source))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn checked(dir: &Path, args: &[&str]) {
        let output = git(dir, args).expect("run Git");
        assert!(
            output.status.success(),
            "Git failed: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }

    #[test]
    fn import_metadata_forwards_selected_project_defaults() {
        let encoded = import_metadata_json(ImportMetadata {
            name: "Example".into(),
            default_branch: "main".into(),
            recovery_policy: "automatic".into(),
            agent_providers: Some(ImportAgentProviders {
                lead: "codex".into(),
                reviewer: "claude".into(),
            }),
            agent_models: Some(ImportAgentModels {
                lead: "gpt-5.6-sol".into(),
                reviewer: "claude-opus-4-8".into(),
            }),
            autonomy_policy: Some("run_to_completion".into()),
            merge_policy: Some("auto_after_gates".into()),
            dialogue_limits: Some(ImportDialogueLimits {
                planning_rounds: 4,
                implementation_review_rounds: 8,
            }),
        })
        .expect("serialize metadata");

        let value: serde_json::Value = serde_json::from_str(&encoded).expect("parse metadata");
        assert_eq!(value["agent_providers"]["lead"], "codex");
        assert_eq!(value["agent_providers"]["reviewer"], "claude");
        assert_eq!(value["agent_models"]["lead"], "gpt-5.6-sol");
        assert_eq!(value["agent_models"]["reviewer"], "claude-opus-4-8");
        assert_eq!(value["autonomy_policy"], "run_to_completion");
        assert_eq!(value["merge_policy"], "auto_after_gates");
        assert_eq!(value["dialogue_limits"]["planning_rounds"], 4);
        assert_eq!(value["dialogue_limits"]["implementation_review_rounds"], 8);
    }

    #[test]
    fn import_metadata_omits_unspecified_project_defaults() {
        let encoded = import_metadata_json(ImportMetadata {
            name: "Example".into(),
            default_branch: "main".into(),
            recovery_policy: "approval_required".into(),
            agent_providers: None,
            agent_models: None,
            autonomy_policy: None,
            merge_policy: None,
            dialogue_limits: None,
        })
        .expect("serialize metadata");

        let value: serde_json::Value = serde_json::from_str(&encoded).expect("parse metadata");
        assert_eq!(value["name"], "Example");
        assert!(value.get("agent_providers").is_none());
        assert!(value.get("agent_models").is_none());
        assert!(value.get("autonomy_policy").is_none());
        assert!(value.get("merge_policy").is_none());
        assert!(value.get("dialogue_limits").is_none());
    }

    #[test]
    fn source_removal_forgets_only_the_deleted_project_and_is_idempotent() {
        let root = tempfile::tempdir().expect("temp root");
        let path = root.path().join("project-sources.json");
        std::fs::write(
            &path,
            r#"{
              "prj_delete":{"path":"/delete","source_type":"git"},
              "prj_keep":{"path":"/keep","source_type":"plain_folder"}
            }"#,
        )
        .expect("write sources");

        remove_source_at(&path, "prj_delete").expect("remove deleted source");
        remove_source_at(&path, "prj_delete").expect("repeat source removal");
        let value: serde_json::Value =
            serde_json::from_str(&std::fs::read_to_string(&path).expect("read resulting sources"))
                .expect("parse resulting sources");
        assert!(value.get("prj_delete").is_none());
        assert_eq!(value["prj_keep"]["path"], "/keep");
    }

    #[test]
    fn bundles_report_the_exact_import_commit() {
        let root = tempfile::tempdir().expect("temp root");
        let repository = root.path().join("repository");
        std::fs::create_dir(&repository).expect("repository dir");
        checked(&repository, &["init", "--initial-branch=main"]);
        checked(&repository, &["config", "user.name", "Importer"]);
        checked(
            &repository,
            &["config", "user.email", "import@example.test"],
        );
        std::fs::write(repository.join("README.md"), "repo\n").expect("repo file");
        checked(&repository, &["add", "README.md"]);
        checked(&repository, &["commit", "-m", "Initial"]);
        let expected = git_line(&repository, &["rev-parse", "HEAD"]).unwrap();
        let bundle = root.path().join("repository.bundle");
        assert_eq!(
            build_bundle(&repository, "main", &bundle).unwrap(),
            expected
        );

        let folder = root.path().join("folder");
        std::fs::create_dir(&folder).expect("folder dir");
        std::fs::write(folder.join("README.md"), "folder\n").expect("folder file");
        let folder_bundle = root.path().join("folder.bundle");
        let commit = build_bundle(&folder, "main", &folder_bundle).unwrap();
        assert!(matches!(commit.len(), 40 | 64));
        assert!(commit.bytes().all(|byte| byte.is_ascii_hexdigit()));
        assert!(!folder.join(".git").exists());
    }
}
