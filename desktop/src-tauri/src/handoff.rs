//! Trusted host-side synchronization of completed work.
//!
//! The coordinator identifies the exact internal base, reviewed head, and
//! Forgejo merge. This module fetches those objects into a temporary repository,
//! recreates only their net tree change as one user-authored commit, and then
//! fast-forwards the imported source repository. It never pushes upstream and
//! never gives host Git credentials to a container.

use serde::{Deserialize, Serialize};
use std::fs::File;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use std::sync::Mutex;
use tauri::{AppHandle, Manager};

const COORDINATOR_BASE: &str = "http://127.0.0.1:8080";
const FORGEJO_BASE: &str = "http://127.0.0.1:3001";
const RECEIPTS_FILE: &str = "handoff-receipts.json";
static HANDOFF_LOCK: Mutex<()> = Mutex::new(());

#[derive(Clone, Debug, Deserialize, PartialEq)]
struct CompletedHandoff {
    project_id: String,
    feature_id: String,
    source: HandoffSource,
    pull_request: HandoffPullRequest,
    merged_at: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq)]
struct HandoffSource {
    repository: HandoffRepository,
    base_branch: String,
    feature_branch: String,
    base_commit_id: String,
    approved_commit_id: String,
    merge_commit_id: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq)]
struct HandoffRepository {
    owner: String,
    name: String,
}

#[derive(Clone, Debug, Deserialize, PartialEq)]
struct HandoffPullRequest {
    number: i64,
    url: String,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
struct HandoffReceipt {
    project_id: String,
    feature_id: String,
    internal_repository_owner: String,
    internal_repository_name: String,
    internal_base_commit_id: String,
    internal_approved_commit_id: String,
    internal_merge_commit_id: String,
    destination_repository_path: String,
    target_branch: String,
    local_base_commit_id: String,
    local_commit_id: String,
    commit_message: String,
    author_name: String,
    author_email: String,
}

#[derive(Debug, Default, Deserialize, Serialize)]
struct HandoffReceipts {
    version: u32,
    receipts: Vec<HandoffReceipt>,
}

#[derive(Debug, Serialize)]
pub struct SynchronizeResult {
    project_id: String,
    feature_id: String,
    repository_path: String,
    target_branch: String,
    local_commit_id: String,
    created: bool,
}

#[derive(Debug)]
struct GitIdentity {
    name: String,
    email: String,
}

/// Synchronize one completed work order into its originally imported Git
/// repository. This command intentionally has no push option.
#[tauri::command]
pub async fn synchronize_feature_locally(
    app: AppHandle,
    project_id: String,
    feature_id: String,
    commit_message: String,
) -> Result<SynchronizeResult, String> {
    validate_identifier("project ID", &project_id)?;
    validate_identifier("feature ID", &feature_id)?;
    validate_text("commit message", &commit_message, 4096)?;

    let handoff = fetch_handoff(&project_id, &feature_id).await?;
    if handoff.project_id != project_id || handoff.feature_id != feature_id {
        return Err("coordinator returned a handoff for another work order".into());
    }
    let source = super::import::get_project_source(app.clone(), project_id.clone())?
        .ok_or("this project has no trusted local source mapping")?;
    let receipts_path = receipts_path(&app)?;
    // An exact retry can be verified from the trusted receipt and local Git
    // without Forgejo. Missing authentication is therefore carried into the
    // blocking operation and rejected only if a new fetch is actually needed.
    let token = forgejo_token_path()
        .ok()
        .and_then(|path| std::fs::read_to_string(path).ok())
        .map(|value| value.trim().to_string())
        .filter(|value| !value.is_empty());
    let internal_url = internal_repository_url(&handoff.source.repository)?;

    tokio::task::spawn_blocking(move || {
        let _guard = HANDOFF_LOCK
            .lock()
            .map_err(|_| "the local handoff lock is unavailable".to_string())?;
        synchronize_repository(
            Path::new(&source),
            &receipts_path,
            &handoff,
            &commit_message,
            &internal_url,
            token.as_deref(),
        )
    })
    .await
    .map_err(|e| format!("local synchronization task failed: {e}"))?
}

async fn fetch_handoff(project_id: &str, feature_id: &str) -> Result<CompletedHandoff, String> {
    let response = reqwest::Client::new()
        .get(format!(
            "{COORDINATOR_BASE}/api/v1/projects/{project_id}/features/{feature_id}/handoff"
        ))
        .send()
        .await
        .map_err(|e| format!("request completed handoff from coordinator: {e}"))?;
    let status = response.status();
    let body = response.text().await.unwrap_or_default();
    if !status.is_success() {
        if let Ok(value) = serde_json::from_str::<serde_json::Value>(&body) {
            if let Some(message) = value.pointer("/error/message").and_then(|v| v.as_str()) {
                return Err(message.to_string());
            }
        }
        return Err(format!(
            "completed handoff request failed (HTTP {})",
            status.as_u16()
        ));
    }
    serde_json::from_str(&body).map_err(|e| format!("parse completed handoff: {e}"))
}

fn synchronize_repository(
    source_path: &Path,
    receipts_path: &Path,
    handoff: &CompletedHandoff,
    commit_message: &str,
    internal_url: &str,
    token: Option<&str>,
) -> Result<SynchronizeResult, String> {
    validate_handoff(handoff)?;
    validate_text("commit message", commit_message, 4096)?;
    let source_path = std::fs::canonicalize(source_path)
        .map_err(|e| format!("resolve imported source repository: {e}"))?;
    if !super::import::is_repo_root(&source_path) {
        return Err(
            "local synchronization currently requires a project imported from a Git repository"
                .into(),
        );
    }
    require_clean_target(&source_path, &handoff.source.base_branch)?;

    let mut receipts = load_receipts(receipts_path)?;
    let existing: Vec<&HandoffReceipt> = receipts
        .receipts
        .iter()
        .filter(|receipt| {
            receipt.project_id == handoff.project_id && receipt.feature_id == handoff.feature_id
        })
        .collect();
    if existing.len() > 1 {
        return Err("more than one trusted handoff record exists for this work order".into());
    }
    if let Some(receipt) = existing.first() {
        verify_existing_receipt(receipt, &source_path, handoff, commit_message)?;
        return Ok(result_from_receipt(receipt, false));
    }

    let identity = read_git_identity(&source_path)?;
    let local_base = resolve_local_base(&receipts, &source_path, handoff)?;
    let temporary = tempfile::tempdir().map_err(|e| format!("create handoff temp dir: {e}"))?;
    let internal_repo = temporary.path().join("internal.git");
    let local_clone = temporary.path().join("local");
    let patch_path = temporary.path().join("approved.patch");

    prepare_internal_repository(&internal_repo, internal_url, handoff, token)?;
    verify_internal_history(&internal_repo, handoff)?;
    let approved_tree = git_dir_line(
        &internal_repo,
        &[
            "rev-parse",
            &format!("{}^{{tree}}", handoff.source.approved_commit_id),
        ],
    )?;
    prepare_local_clone(&source_path, &local_clone, &local_base)?;
    verify_matching_base_trees(&source_path, &internal_repo, &local_base, handoff)?;
    write_approved_patch(&internal_repo, handoff, &patch_path)?;
    apply_patch(&local_clone, &patch_path)?;
    let local_commit = create_clean_commit(
        &local_clone,
        &local_base,
        handoff,
        commit_message,
        &identity,
        &approved_tree,
    )?;

    import_clean_commit(&source_path, &local_clone, &local_commit)?;
    let moved = install_clean_commit(
        &source_path,
        &handoff.source.base_branch,
        &local_base,
        &local_commit,
    )?;

    let receipt = HandoffReceipt {
        project_id: handoff.project_id.clone(),
        feature_id: handoff.feature_id.clone(),
        internal_repository_owner: handoff.source.repository.owner.clone(),
        internal_repository_name: handoff.source.repository.name.clone(),
        internal_base_commit_id: handoff.source.base_commit_id.clone(),
        internal_approved_commit_id: handoff.source.approved_commit_id.clone(),
        internal_merge_commit_id: handoff.source.merge_commit_id.clone(),
        destination_repository_path: source_path.to_string_lossy().into_owned(),
        target_branch: handoff.source.base_branch.clone(),
        local_base_commit_id: local_base,
        local_commit_id: local_commit,
        commit_message: commit_message.to_string(),
        author_name: identity.name,
        author_email: identity.email,
    };
    receipts.receipts.push(receipt.clone());
    save_receipts(receipts_path, &receipts)?;
    Ok(result_from_receipt(&receipt, moved))
}

fn validate_handoff(handoff: &CompletedHandoff) -> Result<(), String> {
    validate_identifier("handoff project ID", &handoff.project_id)?;
    validate_identifier("handoff feature ID", &handoff.feature_id)?;
    validate_coordinate("repository owner", &handoff.source.repository.owner)?;
    validate_coordinate("repository name", &handoff.source.repository.name)?;
    validate_text("base branch", &handoff.source.base_branch, 256)?;
    validate_text("feature branch", &handoff.source.feature_branch, 256)?;
    for (name, value) in [
        ("base commit", handoff.source.base_commit_id.as_str()),
        (
            "approved commit",
            handoff.source.approved_commit_id.as_str(),
        ),
        ("merge commit", handoff.source.merge_commit_id.as_str()),
    ] {
        if !valid_object_id(value) {
            return Err(format!("{name} is not a valid Git object ID"));
        }
    }
    if handoff.pull_request.number < 1 || handoff.pull_request.url.trim().is_empty() {
        return Err("completed handoff has no valid pull request identity".into());
    }
    validate_text("merge time", &handoff.merged_at, 128)?;
    Ok(())
}

fn validate_text(name: &str, value: &str, maximum: usize) -> Result<(), String> {
    if value.is_empty() || value != value.trim() || value.len() > maximum || value.contains('\0') {
        return Err(format!(
            "{name} is required, trimmed, and at most {maximum} bytes"
        ));
    }
    Ok(())
}

fn validate_identifier(name: &str, value: &str) -> Result<(), String> {
    validate_text(name, value, 256)?;
    if !value
        .bytes()
        .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'_' | b'-'))
    {
        return Err(format!("{name} contains unsupported characters"));
    }
    Ok(())
}

fn validate_coordinate(name: &str, value: &str) -> Result<(), String> {
    validate_text(name, value, 128)?;
    if value == "."
        || value == ".."
        || !value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'.' | b'_' | b'-'))
    {
        return Err(format!("{name} contains unsupported characters"));
    }
    Ok(())
}

fn valid_object_id(value: &str) -> bool {
    matches!(value.len(), 40 | 64)
        && value
            .bytes()
            .all(|byte| byte.is_ascii_hexdigit() && !byte.is_ascii_uppercase())
}

fn require_clean_target(repository: &Path, branch: &str) -> Result<(), String> {
    check_branch_name(repository, branch)?;
    let root = git_line(repository, &["rev-parse", "--show-toplevel"])?;
    let canonical_root =
        std::fs::canonicalize(&root).map_err(|e| format!("resolve local repository root: {e}"))?;
    if canonical_root != repository {
        return Err("the recorded source is no longer the repository root".into());
    }
    let current = git_line(repository, &["symbolic-ref", "--quiet", "--short", "HEAD"])?;
    if current != branch {
        return Err(format!(
            "check out the local {branch} branch before synchronizing this work order"
        ));
    }
    let status = git_line_allow_empty(
        repository,
        &["status", "--porcelain=v1", "--untracked-files=all"],
    )?;
    if !status.is_empty() {
        return Err("the local repository has uncommitted or untracked files".into());
    }
    Ok(())
}

fn read_git_identity(repository: &Path) -> Result<GitIdentity, String> {
    let name = git_line(repository, &["config", "--get", "user.name"])
        .map_err(|_| "configure a Git user.name before local synchronization".to_string())?;
    let email = git_line(repository, &["config", "--get", "user.email"])
        .map_err(|_| "configure a Git user.email before local synchronization".to_string())?;
    validate_identity("Git user.name", &name)?;
    validate_identity("Git user.email", &email)?;
    Ok(GitIdentity { name, email })
}

fn validate_identity(name: &str, value: &str) -> Result<(), String> {
    validate_text(name, value, 512)?;
    if value.contains(['\r', '\n']) {
        return Err(format!("{name} cannot contain a line break"));
    }
    Ok(())
}

fn resolve_local_base(
    receipts: &HandoffReceipts,
    repository: &Path,
    handoff: &CompletedHandoff,
) -> Result<String, String> {
    let candidates: Vec<&HandoffReceipt> = receipts
        .receipts
        .iter()
        .filter(|receipt| {
            receipt.project_id == handoff.project_id
                && receipt.internal_merge_commit_id == handoff.source.base_commit_id
        })
        .collect();
    if candidates.len() > 1 {
        return Err("more than one prior handoff claims this internal base".into());
    }
    let local_base = if let Some(prior) = candidates.first() {
        if prior.destination_repository_path != repository.to_string_lossy()
            || prior.target_branch != handoff.source.base_branch
        {
            return Err("the prior handoff maps this base to another destination".into());
        }
        prior.local_commit_id.clone()
    } else {
        handoff.source.base_commit_id.clone()
    };
    require_commit(repository, &local_base, "expected local base")?;
    Ok(local_base)
}

fn prepare_internal_repository(
    repository: &Path,
    remote_url: &str,
    handoff: &CompletedHandoff,
    token: Option<&str>,
) -> Result<(), String> {
    run_checked(
        Command::new("git")
            .arg("init")
            .arg("--bare")
            .arg(repository),
        "initialize temporary internal repository",
    )?;
    for branch in [
        handoff.source.base_branch.as_str(),
        handoff.source.feature_branch.as_str(),
    ] {
        let reference = format!("refs/heads/{branch}");
        run_git_dir_checked(
            repository,
            &["check-ref-format", &reference],
            "validate internal branch identity",
        )?;
    }
    let feature_ref = format!(
        "+refs/heads/{}:refs/heads/commitarium-feature",
        handoff.source.feature_branch
    );
    let base_ref = format!(
        "+refs/heads/{}:refs/heads/commitarium-base",
        handoff.source.base_branch
    );
    let mut command = Command::new("git");
    command
        .arg("--git-dir")
        .arg(repository)
        .args(["fetch", "--no-tags", "--force", "--", remote_url])
        .arg(feature_ref)
        .arg(base_ref)
        .env("GIT_TERMINAL_PROMPT", "0");
    if let Some(token) = token {
        command
            .env("GIT_CONFIG_COUNT", "2")
            .env("GIT_CONFIG_KEY_0", "http.extraHeader")
            .env(
                "GIT_CONFIG_VALUE_0",
                format!("Authorization: token {token}"),
            )
            .env("GIT_CONFIG_KEY_1", "credential.helper")
            .env("GIT_CONFIG_VALUE_1", "");
    } else if remote_url.starts_with("http://") || remote_url.starts_with("https://") {
        return Err("internal Forgejo token not found; set COMMITARIUM_FORGEJO_TOKEN_FILE".into());
    }
    run_checked(
        &mut command,
        "fetch exact completed work from internal Forgejo",
    )?;
    Ok(())
}

fn verify_internal_history(repository: &Path, handoff: &CompletedHandoff) -> Result<(), String> {
    let feature = git_dir_line(repository, &["rev-parse", "refs/heads/commitarium-feature"])?;
    if feature != handoff.source.approved_commit_id {
        return Err("the internal feature branch no longer points to the approved commit".into());
    }
    for (name, commit) in [
        ("base", handoff.source.base_commit_id.as_str()),
        ("approved", handoff.source.approved_commit_id.as_str()),
        ("merge", handoff.source.merge_commit_id.as_str()),
    ] {
        require_commit_in_git_dir(repository, commit, name)?;
    }
    require_ancestor(
        repository,
        &handoff.source.base_commit_id,
        &handoff.source.approved_commit_id,
        "the approved commit does not descend from the recorded internal base",
    )?;
    require_ancestor(
        repository,
        &handoff.source.approved_commit_id,
        &handoff.source.merge_commit_id,
        "the recorded Forgejo merge does not contain the approved commit",
    )?;
    let base_tree = git_dir_line(
        repository,
        &[
            "rev-parse",
            &format!("{}^{{tree}}", handoff.source.base_commit_id),
        ],
    )?;
    let approved_tree = git_dir_line(
        repository,
        &[
            "rev-parse",
            &format!("{}^{{tree}}", handoff.source.approved_commit_id),
        ],
    )?;
    if base_tree == approved_tree {
        return Err("the approved work order contains no net source change".into());
    }
    Ok(())
}

fn prepare_local_clone(source: &Path, clone: &Path, base: &str) -> Result<(), String> {
    run_checked(
        Command::new("git")
            .args(["clone", "--quiet", "--no-hardlinks", "--no-checkout", "--"])
            .arg(source)
            .arg(clone),
        "create temporary local checkout",
    )?;
    run_git_checked(
        clone,
        &["checkout", "--quiet", "--detach", base],
        "check out local base",
    )?;
    Ok(())
}

fn verify_matching_base_trees(
    source: &Path,
    internal: &Path,
    local_base: &str,
    handoff: &CompletedHandoff,
) -> Result<(), String> {
    let local_tree = git_line(source, &["rev-parse", &format!("{local_base}^{{tree}}")])?;
    let internal_tree = git_dir_line(
        internal,
        &[
            "rev-parse",
            &format!("{}^{{tree}}", handoff.source.base_commit_id),
        ],
    )?;
    if local_tree != internal_tree {
        return Err("the mapped local base content differs from the internal feature base".into());
    }
    Ok(())
}

fn write_approved_patch(
    internal: &Path,
    handoff: &CompletedHandoff,
    patch_path: &Path,
) -> Result<(), String> {
    let patch = File::create(patch_path).map_err(|e| format!("create temporary patch: {e}"))?;
    let output = Command::new("git")
        .arg("--git-dir")
        .arg(internal)
        .args([
            "diff",
            "--binary",
            "--full-index",
            "--no-ext-diff",
            "--no-textconv",
            "--no-renames",
        ])
        .arg(&handoff.source.base_commit_id)
        .arg(&handoff.source.approved_commit_id)
        .stdout(Stdio::from(patch))
        .stderr(Stdio::piped())
        .output()
        .map_err(|e| format!("generate reviewed source change: {e}"))?;
    require_success(output, "generate reviewed source change")
}

fn apply_patch(repository: &Path, patch_path: &Path) -> Result<(), String> {
    run_checked(
        Command::new("git")
            .arg("-C")
            .arg(repository)
            .args(["apply", "--index", "--binary", "--whitespace=nowarn", "--"])
            .arg(patch_path),
        "apply reviewed source change to temporary checkout",
    )
}

fn create_clean_commit(
    repository: &Path,
    local_base: &str,
    handoff: &CompletedHandoff,
    message: &str,
    identity: &GitIdentity,
    expected_tree: &str,
) -> Result<String, String> {
    let approved_tree = git_line(repository, &["write-tree"])?;
    let base_tree = git_line(repository, &["rev-parse", "HEAD^{tree}"])?;
    if approved_tree == base_tree {
        return Err("the reviewed source change produced an empty local commit".into());
    }
    if approved_tree != expected_tree {
        return Err(
            "the recreated local source tree differs from the approved Forgejo tree".into(),
        );
    }
    let mut command = Command::new("git");
    command
        .arg("-C")
        .arg(repository)
        .args([
            "-c",
            "core.hooksPath=/dev/null",
            "-c",
            "commit.gpgSign=false",
        ])
        .args([
            "commit",
            "--quiet",
            "--no-verify",
            "--no-gpg-sign",
            "-m",
            message,
        ])
        .env("GIT_AUTHOR_NAME", &identity.name)
        .env("GIT_AUTHOR_EMAIL", &identity.email)
        .env("GIT_COMMITTER_NAME", &identity.name)
        .env("GIT_COMMITTER_EMAIL", &identity.email)
        .env("GIT_AUTHOR_DATE", &handoff.merged_at)
        .env("GIT_COMMITTER_DATE", &handoff.merged_at);
    run_checked(&mut command, "create clean local handoff commit")?;
    let commit = git_line(repository, &["rev-parse", "HEAD"])?;
    let parent = git_line(repository, &["rev-parse", "HEAD^"])?;
    if parent != local_base {
        return Err("the clean handoff commit has an unexpected parent".into());
    }
    let committed_tree = git_line(repository, &["rev-parse", "HEAD^{tree}"])?;
    if committed_tree != expected_tree {
        return Err("the clean handoff commit changed after staging".into());
    }
    Ok(commit)
}

fn import_clean_commit(source: &Path, clone: &Path, commit: &str) -> Result<(), String> {
    run_checked(
        Command::new("git")
            .arg("-C")
            .arg(source)
            .args([
                "fetch",
                "--quiet",
                "--no-tags",
                "--no-write-fetch-head",
                "--",
            ])
            .arg(clone)
            .arg(commit),
        "copy clean handoff commit into local repository",
    )
}

fn install_clean_commit(
    repository: &Path,
    branch: &str,
    expected_base: &str,
    commit: &str,
) -> Result<bool, String> {
    require_clean_target(repository, branch)?;
    let head = git_line(repository, &["rev-parse", "HEAD"])?;
    if head == commit {
        return Ok(false);
    }
    if head != expected_base {
        return Err("the local target branch advanced while synchronization was prepared".into());
    }
    run_checked(
        Command::new("git")
            .arg("-C")
            .arg(repository)
            .args(["-c", "core.hooksPath=/dev/null"])
            .args(["merge", "--quiet", "--ff-only", "--no-stat", commit]),
        "fast-forward local target branch to clean handoff commit",
    )?;
    let installed = git_line(repository, &["rev-parse", "HEAD"])?;
    if installed != commit {
        return Err("local target branch did not reach the clean handoff commit".into());
    }
    require_clean_target(repository, branch)?;
    Ok(true)
}

fn verify_existing_receipt(
    receipt: &HandoffReceipt,
    repository: &Path,
    handoff: &CompletedHandoff,
    commit_message: &str,
) -> Result<(), String> {
    let exact = receipt.project_id == handoff.project_id
        && receipt.internal_repository_owner == handoff.source.repository.owner
        && receipt.internal_repository_name == handoff.source.repository.name
        && receipt.internal_base_commit_id == handoff.source.base_commit_id
        && receipt.internal_approved_commit_id == handoff.source.approved_commit_id
        && receipt.internal_merge_commit_id == handoff.source.merge_commit_id
        && receipt.destination_repository_path == repository.to_string_lossy()
        && receipt.target_branch == handoff.source.base_branch
        && receipt.commit_message == commit_message;
    if !exact {
        return Err("an existing local handoff record disagrees with this request".into());
    }
    require_commit(
        repository,
        &receipt.local_commit_id,
        "recorded local handoff",
    )?;
    let parent = git_line(
        repository,
        &["rev-parse", &format!("{}^", receipt.local_commit_id)],
    )?;
    if parent != receipt.local_base_commit_id {
        return Err("the recorded local handoff has an unexpected parent".into());
    }
    let branch_head = git_line(repository, &["rev-parse", "HEAD"])?;
    let status = Command::new("git")
        .arg("-C")
        .arg(repository)
        .args([
            "merge-base",
            "--is-ancestor",
            &receipt.local_commit_id,
            &branch_head,
        ])
        .status()
        .map_err(|e| format!("verify recorded local handoff ancestry: {e}"))?;
    if !status.success() {
        return Err(
            "the local target branch no longer contains the recorded handoff commit".into(),
        );
    }
    Ok(())
}

fn result_from_receipt(receipt: &HandoffReceipt, created: bool) -> SynchronizeResult {
    SynchronizeResult {
        project_id: receipt.project_id.clone(),
        feature_id: receipt.feature_id.clone(),
        repository_path: receipt.destination_repository_path.clone(),
        target_branch: receipt.target_branch.clone(),
        local_commit_id: receipt.local_commit_id.clone(),
        created,
    }
}

fn load_receipts(path: &Path) -> Result<HandoffReceipts, String> {
    match std::fs::read_to_string(path) {
        Ok(contents) => {
            let receipts: HandoffReceipts = serde_json::from_str(&contents)
                .map_err(|e| format!("parse trusted handoff records: {e}"))?;
            if receipts.version != 1 {
                return Err("trusted handoff record version is unsupported".into());
            }
            for receipt in &receipts.receipts {
                validate_receipt(receipt)?;
            }
            Ok(receipts)
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(HandoffReceipts {
            version: 1,
            receipts: Vec::new(),
        }),
        Err(error) => Err(format!("read trusted handoff records: {error}")),
    }
}

fn validate_receipt(receipt: &HandoffReceipt) -> Result<(), String> {
    validate_identifier("receipt project ID", &receipt.project_id)?;
    validate_identifier("receipt feature ID", &receipt.feature_id)?;
    validate_coordinate(
        "receipt repository owner",
        &receipt.internal_repository_owner,
    )?;
    validate_coordinate("receipt repository name", &receipt.internal_repository_name)?;
    for (name, value) in [
        (
            "receipt internal base",
            receipt.internal_base_commit_id.as_str(),
        ),
        (
            "receipt internal approved commit",
            receipt.internal_approved_commit_id.as_str(),
        ),
        (
            "receipt internal merge commit",
            receipt.internal_merge_commit_id.as_str(),
        ),
        ("receipt local base", receipt.local_base_commit_id.as_str()),
        ("receipt local commit", receipt.local_commit_id.as_str()),
    ] {
        if !valid_object_id(value) {
            return Err(format!("{name} is not a valid Git object ID"));
        }
    }
    validate_text(
        "receipt destination repository",
        &receipt.destination_repository_path,
        32 * 1024,
    )?;
    validate_text("receipt target branch", &receipt.target_branch, 256)?;
    validate_text("receipt commit message", &receipt.commit_message, 4096)?;
    validate_identity("receipt author name", &receipt.author_name)?;
    validate_identity("receipt author email", &receipt.author_email)?;
    Ok(())
}

fn save_receipts(path: &Path, receipts: &HandoffReceipts) -> Result<(), String> {
    let parent = path
        .parent()
        .ok_or("trusted handoff record path has no parent")?;
    std::fs::create_dir_all(parent)
        .map_err(|e| format!("create trusted handoff record directory: {e}"))?;
    let contents = serde_json::to_vec_pretty(receipts)
        .map_err(|e| format!("encode trusted handoff records: {e}"))?;
    let mut temporary = tempfile::NamedTempFile::new_in(parent)
        .map_err(|e| format!("create temporary handoff record: {e}"))?;
    temporary
        .write_all(&contents)
        .and_then(|_| temporary.as_file().sync_all())
        .map_err(|e| format!("write trusted handoff records: {e}"))?;
    temporary
        .persist(path)
        .map_err(|e| format!("replace trusted handoff records: {}", e.error))?;
    Ok(())
}

fn receipts_path(app: &AppHandle) -> Result<PathBuf, String> {
    let directory = app
        .path()
        .app_data_dir()
        .map_err(|e| format!("resolve app data directory: {e}"))?;
    Ok(directory.join(RECEIPTS_FILE))
}

fn forgejo_token_path() -> Result<PathBuf, String> {
    if let Ok(configured) = std::env::var("COMMITARIUM_FORGEJO_TOKEN_FILE") {
        let path = PathBuf::from(configured);
        if path.is_file() {
            return Ok(path);
        }
        return Err("COMMITARIUM_FORGEJO_TOKEN_FILE does not name a file".into());
    }
    if let Ok(compose) = std::env::var("COMMITARIUM_COMPOSE_FILE") {
        if let Some(parent) = Path::new(&compose).parent() {
            let candidate = parent.join(".commitarium/forgejo-token");
            if candidate.is_file() {
                return Ok(candidate);
            }
        }
    }
    let mut directory = std::env::current_dir().map_err(|e| format!("current directory: {e}"))?;
    loop {
        let candidate = directory.join(".commitarium/forgejo-token");
        if candidate.is_file() {
            return Ok(candidate);
        }
        if !directory.pop() {
            return Err(
                "internal Forgejo token not found; set COMMITARIUM_FORGEJO_TOKEN_FILE".into(),
            );
        }
    }
}

fn internal_repository_url(repository: &HandoffRepository) -> Result<String, String> {
    validate_coordinate("repository owner", &repository.owner)?;
    validate_coordinate("repository name", &repository.name)?;
    Ok(format!(
        "{FORGEJO_BASE}/{}/{}.git",
        repository.owner, repository.name
    ))
}

fn check_branch_name(repository: &Path, branch: &str) -> Result<(), String> {
    validate_text("target branch", branch, 256)?;
    let full = format!("refs/heads/{branch}");
    run_git_checked(
        repository,
        &["check-ref-format", &full],
        "validate target branch",
    )
}

fn require_commit(repository: &Path, commit: &str, name: &str) -> Result<(), String> {
    let object = format!("{commit}^{{commit}}");
    run_git_checked(repository, &["cat-file", "-e", &object], name)
}

fn require_commit_in_git_dir(repository: &Path, commit: &str, name: &str) -> Result<(), String> {
    let object = format!("{commit}^{{commit}}");
    run_git_dir_checked(repository, &["cat-file", "-e", &object], name)
}

fn require_ancestor(
    repository: &Path,
    ancestor: &str,
    descendant: &str,
    message: &str,
) -> Result<(), String> {
    let status = Command::new("git")
        .arg("--git-dir")
        .arg(repository)
        .args(["merge-base", "--is-ancestor", ancestor, descendant])
        .status()
        .map_err(|e| format!("verify internal history: {e}"))?;
    if !status.success() {
        return Err(message.into());
    }
    Ok(())
}

fn git_line(repository: &Path, args: &[&str]) -> Result<String, String> {
    let output = Command::new("git")
        .arg("-C")
        .arg(repository)
        .args(args)
        .output()
        .map_err(|e| format!("run git {}: {e}", args.join(" ")))?;
    output_line(output, args)
}

fn git_line_allow_empty(repository: &Path, args: &[&str]) -> Result<String, String> {
    let output = Command::new("git")
        .arg("-C")
        .arg(repository)
        .args(args)
        .output()
        .map_err(|e| format!("run git {}: {e}", args.join(" ")))?;
    if !output.status.success() {
        return Err(git_failure(args, &output));
    }
    Ok(String::from_utf8_lossy(&output.stdout).trim().to_string())
}

fn git_dir_line(repository: &Path, args: &[&str]) -> Result<String, String> {
    let output = Command::new("git")
        .arg("--git-dir")
        .arg(repository)
        .args(args)
        .output()
        .map_err(|e| format!("run git {}: {e}", args.join(" ")))?;
    output_line(output, args)
}

fn output_line(output: Output, args: &[&str]) -> Result<String, String> {
    if !output.status.success() {
        return Err(git_failure(args, &output));
    }
    let value = String::from_utf8_lossy(&output.stdout).trim().to_string();
    if value.is_empty() {
        return Err(format!("git {} returned no value", args.join(" ")));
    }
    Ok(value)
}

fn git_failure(args: &[&str], output: &Output) -> String {
    let stderr = String::from_utf8_lossy(&output.stderr);
    format!("git {} failed: {}", args.join(" "), stderr.trim())
}

fn run_git_checked(repository: &Path, args: &[&str], action: &str) -> Result<(), String> {
    run_checked(
        Command::new("git").arg("-C").arg(repository).args(args),
        action,
    )
}

fn run_git_dir_checked(repository: &Path, args: &[&str], action: &str) -> Result<(), String> {
    run_checked(
        Command::new("git")
            .arg("--git-dir")
            .arg(repository)
            .args(args),
        action,
    )
}

fn run_checked(command: &mut Command, action: &str) -> Result<(), String> {
    let output = command.output().map_err(|e| format!("{action}: {e}"))?;
    require_success(output, action)
}

fn require_success(output: Output, action: &str) -> Result<(), String> {
    if output.status.success() {
        Ok(())
    } else {
        Err(format!(
            "{action}: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    struct GitFixture {
        _root: tempfile::TempDir,
        source: PathBuf,
        internal: PathBuf,
        internal_work: PathBuf,
        receipts: PathBuf,
        initial: String,
    }

    fn fixture() -> GitFixture {
        let root = tempfile::tempdir().expect("temp root");
        let source = root.path().join("source");
        let internal = root.path().join("internal.git");
        let internal_work = root.path().join("internal-work");
        let receipts = root.path().join("state/handoff-receipts.json");
        std::fs::create_dir(&source).expect("source dir");
        command(&source, &["init", "--initial-branch=main"]);
        command(&source, &["config", "user.name", "Local User"]);
        command(&source, &["config", "user.email", "local@example.test"]);
        std::fs::write(source.join("README.md"), "initial\n").expect("initial file");
        command(&source, &["add", "README.md"]);
        command(&source, &["commit", "-m", "Initial source"]);
        let initial = line(&source, &["rev-parse", "HEAD"]);

        checked(
            Command::new("git")
                .args(["clone", "--quiet", "--bare", "--"])
                .arg(&source)
                .arg(&internal),
        );
        checked(
            Command::new("git")
                .args(["clone", "--quiet", "--"])
                .arg(&internal)
                .arg(&internal_work),
        );
        command(&internal_work, &["config", "user.name", "Agent Author"]);
        command(
            &internal_work,
            &["config", "user.email", "agent@commitarium.test"],
        );
        GitFixture {
            _root: root,
            source,
            internal,
            internal_work,
            receipts,
            initial,
        }
    }

    fn add_internal_feature(
        fixture: &GitFixture,
        branch: &str,
        file: &str,
        contents: &str,
    ) -> CompletedHandoff {
        command(&fixture.internal_work, &["checkout", "--quiet", "main"]);
        command(
            &fixture.internal_work,
            &["pull", "--quiet", "--ff-only", "origin", "main"],
        );
        let base = line(&fixture.internal_work, &["rev-parse", "HEAD"]);
        command(
            &fixture.internal_work,
            &["checkout", "--quiet", "-b", branch],
        );
        std::fs::write(fixture.internal_work.join(file), contents).expect("feature file");
        command(&fixture.internal_work, &["add", file]);
        command(
            &fixture.internal_work,
            &["commit", "-m", "Agent implementation"],
        );
        let approved = line(&fixture.internal_work, &["rev-parse", "HEAD"]);
        command(
            &fixture.internal_work,
            &["push", "--quiet", "origin", branch],
        );
        command(&fixture.internal_work, &["checkout", "--quiet", "main"]);
        command(
            &fixture.internal_work,
            &[
                "merge",
                "--quiet",
                "--no-ff",
                branch,
                "-m",
                "Internal merge",
            ],
        );
        let merged = line(&fixture.internal_work, &["rev-parse", "HEAD"]);
        command(
            &fixture.internal_work,
            &["push", "--quiet", "origin", "main"],
        );
        CompletedHandoff {
            project_id: "prj_test".into(),
            feature_id: format!("fea_{}", branch.replace('/', "_")),
            source: HandoffSource {
                repository: HandoffRepository {
                    owner: "commitarium".into(),
                    name: "project".into(),
                },
                base_branch: "main".into(),
                feature_branch: branch.into(),
                base_commit_id: base,
                approved_commit_id: approved,
                merge_commit_id: merged,
            },
            pull_request: HandoffPullRequest {
                number: 7,
                url: "http://127.0.0.1:3001/commitarium/project/pulls/7".into(),
            },
            merged_at: "2026-09-11T14:00:00Z".into(),
        }
    }

    #[test]
    fn synchronizes_exact_tree_as_one_local_user_commit_and_retries() {
        let fixture = fixture();
        let handoff =
            add_internal_feature(&fixture, "commitarium/feature-one", "feature.txt", "done\n");
        let internal_url = fixture.internal.to_string_lossy();

        let created = synchronize_repository(
            &fixture.source,
            &fixture.receipts,
            &handoff,
            "Complete feature one",
            &internal_url,
            None,
        )
        .expect("synchronize");
        assert!(created.created);
        assert_eq!(
            created.local_commit_id,
            line(&fixture.source, &["rev-parse", "HEAD"])
        );
        assert_eq!(
            line(&fixture.source, &["rev-parse", "HEAD^"]),
            fixture.initial
        );
        assert_eq!(
            line(
                &fixture.source,
                &["show", "-s", "--format=%an <%ae>", "HEAD"]
            ),
            "Local User <local@example.test>"
        );
        assert_eq!(
            line(&fixture.source, &["show", "-s", "--format=%s", "HEAD"]),
            "Complete feature one"
        );
        assert_eq!(
            line(&fixture.source, &["rev-parse", "HEAD^{tree}"]),
            git_dir_line(
                &fixture.internal,
                &[
                    "rev-parse",
                    &format!("{}^{{tree}}", handoff.source.approved_commit_id)
                ]
            )
            .expect("approved tree")
        );
        assert!(!Command::new("git")
            .arg("-C")
            .arg(&fixture.source)
            .args([
                "cat-file",
                "-e",
                &format!("{}^{{commit}}", handoff.source.approved_commit_id)
            ])
            .output()
            .expect("inspect agent commit")
            .status
            .success());

        let retried = synchronize_repository(
            &fixture.source,
            &fixture.receipts,
            &handoff,
            "Complete feature one",
            &internal_url,
            None,
        )
        .expect("retry");
        assert!(!retried.created);
        assert_eq!(retried.local_commit_id, created.local_commit_id);
    }

    #[test]
    fn adopts_deterministic_commit_if_receipt_write_was_interrupted() {
        let fixture = fixture();
        let handoff =
            add_internal_feature(&fixture, "commitarium/recovery", "recovered.txt", "safe\n");
        let internal_url = fixture.internal.to_string_lossy();
        let first = synchronize_repository(
            &fixture.source,
            &fixture.receipts,
            &handoff,
            "Recover handoff",
            &internal_url,
            None,
        )
        .expect("first sync");
        std::fs::remove_file(&fixture.receipts).expect("remove receipt to simulate interruption");

        let adopted = synchronize_repository(
            &fixture.source,
            &fixture.receipts,
            &handoff,
            "Recover handoff",
            &internal_url,
            None,
        )
        .expect("adopt deterministic commit");
        assert!(!adopted.created);
        assert_eq!(adopted.local_commit_id, first.local_commit_id);
        assert!(fixture.receipts.is_file());
    }

    #[test]
    fn maps_next_internal_merge_to_previous_clean_local_commit() {
        let fixture = fixture();
        let first = add_internal_feature(&fixture, "commitarium/one", "one.txt", "one\n");
        let internal_url = fixture.internal.to_string_lossy();
        let first_result = synchronize_repository(
            &fixture.source,
            &fixture.receipts,
            &first,
            "Complete one",
            &internal_url,
            None,
        )
        .expect("first sync");
        let second = add_internal_feature(&fixture, "commitarium/two", "two.txt", "two\n");
        assert_eq!(second.source.base_commit_id, first.source.merge_commit_id);

        let second_result = synchronize_repository(
            &fixture.source,
            &fixture.receipts,
            &second,
            "Complete two",
            &internal_url,
            None,
        )
        .expect("second sync");
        assert_eq!(
            line(&fixture.source, &["rev-parse", "HEAD^"]),
            first_result.local_commit_id
        );
        assert_eq!(
            line(&fixture.source, &["rev-parse", "HEAD"]),
            second_result.local_commit_id
        );
    }

    #[test]
    fn refuses_dirty_or_diverged_local_target() {
        let dirty = fixture();
        let dirty_handoff = add_internal_feature(&dirty, "commitarium/dirty", "work.txt", "work\n");
        std::fs::write(dirty.source.join("untracked.txt"), "mine\n").expect("dirty file");
        let error = synchronize_repository(
            &dirty.source,
            &dirty.receipts,
            &dirty_handoff,
            "Dirty target",
            &dirty.internal.to_string_lossy(),
            None,
        )
        .expect_err("dirty repository must fail");
        assert!(error.contains("uncommitted or untracked"), "{error}");

        let diverged = fixture();
        let diverged_handoff =
            add_internal_feature(&diverged, "commitarium/diverged", "work.txt", "work\n");
        std::fs::write(diverged.source.join("local.txt"), "local\n").expect("local file");
        command(&diverged.source, &["add", "local.txt"]);
        command(&diverged.source, &["commit", "-m", "Local divergence"]);
        let local_head = line(&diverged.source, &["rev-parse", "HEAD"]);
        let error = synchronize_repository(
            &diverged.source,
            &diverged.receipts,
            &diverged_handoff,
            "Diverged target",
            &diverged.internal.to_string_lossy(),
            None,
        )
        .expect_err("diverged repository must fail");
        assert!(error.contains("advanced while synchronization"), "{error}");
        assert_eq!(line(&diverged.source, &["rev-parse", "HEAD"]), local_head);
    }

    fn command(repository: &Path, args: &[&str]) {
        run_git_checked(repository, args, &format!("git {}", args.join(" ")))
            .unwrap_or_else(|error| panic!("{error}"));
    }

    fn line(repository: &Path, args: &[&str]) -> String {
        git_line(repository, args).unwrap_or_else(|error| panic!("{error}"))
    }

    fn checked(command: &mut Command) {
        run_checked(command, "test git command").unwrap_or_else(|error| panic!("{error}"));
    }
}
