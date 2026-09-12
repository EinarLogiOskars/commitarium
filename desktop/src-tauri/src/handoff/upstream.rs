//! Publish one verified local handoff commit as a new upstream branch.
//!
//! This module deliberately delegates authentication to the user's system Git
//! configuration. The renderer chooses only an existing remote and a protected
//! `commitarium/` branch name; it never supplies a URL, credential, or commit.

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Output, Stdio};
use tauri::{AppHandle, Manager};

use super::{CompletedHandoff, HandoffReceipt};

const UPSTREAM_RECEIPTS_FILE: &str = "upstream-publication-receipts.json";
const BRANCH_PREFIX: &str = "commitarium/";

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum UpstreamBranchStatus {
    SelectionRequired,
    Ready,
    Published,
    AlreadyPublished,
    BranchConflict,
    AuthenticationRequired,
    RemoteUnavailable,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UpstreamRemote {
    name: String,
    display_location: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct UpstreamBranchResult {
    project_id: String,
    feature_id: String,
    repository_path: String,
    local_commit_id: String,
    remotes: Vec<UpstreamRemote>,
    #[serde(skip_serializing_if = "Option::is_none")]
    selected_remote: Option<String>,
    branch_name: String,
    status: UpstreamBranchStatus,
    created: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    detail: Option<String>,
}

#[derive(Clone, Debug)]
struct RemoteIdentity {
    name: String,
    display_location: String,
    fingerprint: String,
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
struct UpstreamPublicationReceipt {
    project_id: String,
    feature_id: String,
    repository_path: String,
    local_commit_id: String,
    remote_name: String,
    remote_fingerprint: String,
    branch_name: String,
}

#[derive(Debug, Default, Deserialize, Serialize)]
struct UpstreamPublicationReceipts {
    version: u32,
    receipts: Vec<UpstreamPublicationReceipt>,
}

enum RemoteProbe {
    Missing,
    Commit(String),
    Failed(UpstreamBranchStatus),
}

/// Inspect configured remotes and determine whether a protected branch name is
/// available. This command never mutates local or remote Git state.
#[tauri::command]
pub async fn preview_upstream_branch(
    app: AppHandle,
    project_id: String,
    feature_id: String,
    work_order_name: String,
    remote_name: Option<String>,
    branch_name: Option<String>,
) -> Result<UpstreamBranchResult, String> {
    super::validate_identifier("project ID", &project_id)?;
    super::validate_identifier("feature ID", &feature_id)?;
    super::validate_text("work order name", &work_order_name, 512)?;
    let handoff = super::fetch_handoff(&project_id, &feature_id).await?;
    let source = super::super::import::get_project_source(app.clone(), project_id.clone())?
        .ok_or("this project has no trusted local source mapping")?;
    let handoff_receipts = super::receipts_path(&app)?;
    let selected_branch =
        branch_name.unwrap_or_else(|| suggested_branch_name(&work_order_name, &feature_id));

    tokio::task::spawn_blocking(move || {
        let _guard = super::HANDOFF_LOCK
            .lock()
            .map_err(|_| "the local handoff lock is unavailable".to_string())?;
        let (repository, receipt) =
            publication_source(Path::new(&source), &handoff_receipts, &handoff)?;
        preview_repository(
            &repository,
            &receipt,
            remote_name.as_deref(),
            &selected_branch,
        )
    })
    .await
    .map_err(|error| format!("upstream preview task failed: {error}"))?
}

/// Push the exact commit from the trusted local-handoff receipt to a new
/// protected remote branch. No checkout or local reference is changed.
#[tauri::command]
pub async fn publish_upstream_branch(
    app: AppHandle,
    project_id: String,
    feature_id: String,
    remote_name: String,
    branch_name: String,
) -> Result<UpstreamBranchResult, String> {
    super::validate_identifier("project ID", &project_id)?;
    super::validate_identifier("feature ID", &feature_id)?;
    validate_remote_name(&remote_name)?;
    let handoff = super::fetch_handoff(&project_id, &feature_id).await?;
    let source = super::super::import::get_project_source(app.clone(), project_id.clone())?
        .ok_or("this project has no trusted local source mapping")?;
    let handoff_receipts = super::receipts_path(&app)?;
    let publication_receipts = publication_receipts_path(&app)?;

    tokio::task::spawn_blocking(move || {
        let _guard = super::HANDOFF_LOCK
            .lock()
            .map_err(|_| "the local handoff lock is unavailable".to_string())?;
        let (repository, receipt) =
            publication_source(Path::new(&source), &handoff_receipts, &handoff)?;
        publish_repository(
            &repository,
            &publication_receipts,
            &receipt,
            &remote_name,
            &branch_name,
        )
    })
    .await
    .map_err(|error| format!("upstream publication task failed: {error}"))?
}

fn publication_source(
    recorded_source: &Path,
    receipts_path: &Path,
    handoff: &CompletedHandoff,
) -> Result<(PathBuf, HandoffReceipt), String> {
    super::validate_handoff(handoff)?;
    let repository = std::fs::canonicalize(recorded_source)
        .map_err(|error| format!("resolve imported source repository: {error}"))?;
    if !super::super::import::is_repo_root(&repository) {
        return Err(
            "upstream publication requires a project imported from a Git repository".into(),
        );
    }
    let receipts = super::load_receipts(receipts_path)?;
    let matches: Vec<&HandoffReceipt> = receipts
        .receipts
        .iter()
        .filter(|receipt| {
            receipt.project_id == handoff.project_id && receipt.feature_id == handoff.feature_id
        })
        .collect();
    if matches.len() != 1 {
        return Err(if matches.is_empty() {
            "synchronize this completed work order locally before publishing it upstream".into()
        } else {
            "more than one trusted local handoff record exists for this work order".into()
        });
    }
    let receipt = matches[0].clone();
    super::validate_receipt(&receipt)?;
    let exact = receipt.project_id == handoff.project_id
        && receipt.feature_id == handoff.feature_id
        && receipt.internal_repository_owner == handoff.source.repository.owner
        && receipt.internal_repository_name == handoff.source.repository.name
        && receipt.internal_base_commit_id == handoff.source.base_commit_id
        && receipt.internal_approved_commit_id == handoff.source.approved_commit_id
        && receipt.internal_merge_commit_id == handoff.source.merge_commit_id
        && receipt.destination_repository_path == repository.to_string_lossy();
    if !exact {
        return Err("the local handoff receipt disagrees with the completed work order".into());
    }
    verify_recorded_local_commit(&repository, &receipt)?;
    Ok((repository, receipt))
}

fn verify_recorded_local_commit(repository: &Path, receipt: &HandoffReceipt) -> Result<(), String> {
    super::check_branch_name(repository, &receipt.target_branch)?;
    super::require_commit(
        repository,
        &receipt.local_commit_id,
        "recorded local handoff",
    )?;
    let parent = super::git_line(
        repository,
        &["rev-parse", &format!("{}^", receipt.local_commit_id)],
    )?;
    if parent != receipt.local_base_commit_id {
        return Err("the recorded local handoff has an unexpected parent".into());
    }
    let target_ref = format!("refs/heads/{}", receipt.target_branch);
    let target = super::git_line(repository, &["rev-parse", "--verify", &target_ref])?;
    let status = Command::new("git")
        .arg("-C")
        .arg(repository)
        .args([
            "merge-base",
            "--is-ancestor",
            &receipt.local_commit_id,
            &target,
        ])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .map_err(|error| format!("verify recorded local handoff ancestry: {error}"))?;
    if !status.success() {
        return Err(
            "the local target branch no longer contains the recorded handoff commit".into(),
        );
    }
    Ok(())
}

fn preview_repository(
    repository: &Path,
    receipt: &HandoffReceipt,
    selected_remote: Option<&str>,
    branch_name: &str,
) -> Result<UpstreamBranchResult, String> {
    validate_protected_branch(repository, branch_name)?;
    let remotes = configured_remotes(repository)?;
    let remote_name = choose_remote(repository, receipt, &remotes, selected_remote)?;
    let Some(remote_name) = remote_name else {
        return Ok(branch_result(
            receipt,
            &remotes,
            None,
            branch_name,
            UpstreamBranchStatus::SelectionRequired,
            false,
            "Choose one of the repository's configured remotes.",
        ));
    };
    let remote = remotes
        .iter()
        .find(|remote| remote.name == remote_name)
        .ok_or("the selected Git remote is not configured")?;
    Ok(result_for_probe(
        receipt,
        &remotes,
        remote,
        branch_name,
        probe_remote_branch(repository, &remote.name, branch_name),
        false,
    ))
}

fn publish_repository(
    repository: &Path,
    publication_receipts_path: &Path,
    receipt: &HandoffReceipt,
    remote_name: &str,
    branch_name: &str,
) -> Result<UpstreamBranchResult, String> {
    validate_protected_branch(repository, branch_name)?;
    validate_remote_name(remote_name)?;
    let remotes = configured_remotes(repository)?;
    let remote = remotes
        .iter()
        .find(|remote| remote.name == remote_name)
        .ok_or("the selected Git remote is not configured")?;
    let mut publications = load_publication_receipts(publication_receipts_path)?;
    verify_publication_receipts(&publications, receipt, repository, remote, branch_name)?;

    match probe_remote_branch(repository, remote_name, branch_name) {
        RemoteProbe::Commit(commit) if commit == receipt.local_commit_id => {
            record_publication(
                publication_receipts_path,
                &mut publications,
                receipt,
                repository,
                remote,
                branch_name,
            )?;
            return Ok(branch_result(
                receipt,
                &remotes,
                Some(remote_name),
                branch_name,
                UpstreamBranchStatus::AlreadyPublished,
                false,
                "The remote branch already points to the exact synchronized commit.",
            ));
        }
        RemoteProbe::Commit(_) => {
            return Ok(branch_result(
                receipt,
                &remotes,
                Some(remote_name),
                branch_name,
                UpstreamBranchStatus::BranchConflict,
                false,
                "That remote branch already exists with different content.",
            ));
        }
        RemoteProbe::Failed(status) => {
            return Ok(branch_result(
                receipt,
                &remotes,
                Some(remote_name),
                branch_name,
                status,
                false,
                remote_failure_detail(status),
            ));
        }
        RemoteProbe::Missing => {}
    }

    let full_ref = format!("refs/heads/{branch_name}");
    let refspec = format!("{}:{full_ref}", receipt.local_commit_id);
    // An explicit empty lease is an atomic "create only if absent" condition.
    // Although Git names the option force-with-lease, it cannot overwrite an
    // existing reference because the expected old value is the empty value.
    let creation_lease = format!("--force-with-lease={full_ref}:");
    let output = Command::new("git")
        .arg("-C")
        .arg(repository)
        .args([
            "push",
            "--porcelain",
            &creation_lease,
            "--",
            remote_name,
            &refspec,
        ])
        .env("GIT_TERMINAL_PROMPT", "0")
        .stdin(Stdio::null())
        .output()
        .map_err(|error| format!("start system Git push: {error}"))?;

    if !output.status.success() {
        let reprobe = probe_remote_branch(repository, remote_name, branch_name);
        return Ok(match reprobe {
            RemoteProbe::Commit(commit) if commit == receipt.local_commit_id => {
                record_publication(
                    publication_receipts_path,
                    &mut publications,
                    receipt,
                    repository,
                    remote,
                    branch_name,
                )?;
                branch_result(
                    receipt,
                    &remotes,
                    Some(remote_name),
                    branch_name,
                    UpstreamBranchStatus::AlreadyPublished,
                    false,
                    "The remote branch already points to the exact synchronized commit.",
                )
            }
            RemoteProbe::Commit(_) => branch_result(
                receipt,
                &remotes,
                Some(remote_name),
                branch_name,
                UpstreamBranchStatus::BranchConflict,
                false,
                "The branch was created elsewhere before Commitarium could publish it.",
            ),
            RemoteProbe::Failed(status) => branch_result(
                receipt,
                &remotes,
                Some(remote_name),
                branch_name,
                status,
                false,
                remote_failure_detail(status),
            ),
            RemoteProbe::Missing => {
                let status = classify_remote_failure(&output);
                branch_result(
                    receipt,
                    &remotes,
                    Some(remote_name),
                    branch_name,
                    status,
                    false,
                    remote_failure_detail(status),
                )
            }
        });
    }

    match probe_remote_branch(repository, remote_name, branch_name) {
        RemoteProbe::Commit(commit) if commit == receipt.local_commit_id => {}
        RemoteProbe::Commit(_) => {
            return Ok(branch_result(
                receipt,
                &remotes,
                Some(remote_name),
                branch_name,
                UpstreamBranchStatus::BranchConflict,
                false,
                "The new branch does not point to the expected synchronized commit.",
            ));
        }
        RemoteProbe::Failed(status) => {
            return Ok(branch_result(
                receipt,
                &remotes,
                Some(remote_name),
                branch_name,
                status,
                false,
                remote_failure_detail(status),
            ));
        }
        RemoteProbe::Missing => {
            return Ok(branch_result(
                receipt,
                &remotes,
                Some(remote_name),
                branch_name,
                UpstreamBranchStatus::RemoteUnavailable,
                false,
                "System Git reported success but the new branch could not be verified.",
            ));
        }
    }
    record_publication(
        publication_receipts_path,
        &mut publications,
        receipt,
        repository,
        remote,
        branch_name,
    )?;
    Ok(branch_result(
        receipt,
        &remotes,
        Some(remote_name),
        branch_name,
        UpstreamBranchStatus::Published,
        true,
        "The exact synchronized commit was published as a new remote branch.",
    ))
}

fn configured_remotes(repository: &Path) -> Result<Vec<RemoteIdentity>, String> {
    let output = Command::new("git")
        .arg("-C")
        .arg(repository)
        .arg("remote")
        .stdin(Stdio::null())
        .output()
        .map_err(|error| format!("list configured Git remotes: {error}"))?;
    if !output.status.success() {
        return Err("system Git could not list the repository's configured remotes".into());
    }
    let mut remotes = Vec::new();
    for name in String::from_utf8_lossy(&output.stdout).lines() {
        validate_remote_name(name)?;
        let url_output = Command::new("git")
            .arg("-C")
            .arg(repository)
            .args(["remote", "get-url", "--push", "--", name])
            .stdin(Stdio::null())
            .output()
            .map_err(|error| format!("inspect configured Git remote: {error}"))?;
        if !url_output.status.success() {
            return Err(format!("the configured Git remote {name} has no push URL"));
        }
        let push_url = String::from_utf8_lossy(&url_output.stdout)
            .trim()
            .to_string();
        if push_url.is_empty() || push_url.contains(['\r', '\n']) {
            return Err(format!(
                "the configured Git remote {name} has an invalid push URL"
            ));
        }
        remotes.push(RemoteIdentity {
            name: name.to_string(),
            display_location: display_remote_location(&push_url),
            fingerprint: fingerprint(&push_url),
        });
    }
    remotes.sort_by(|left, right| left.name.cmp(&right.name));
    Ok(remotes)
}

fn choose_remote(
    repository: &Path,
    receipt: &HandoffReceipt,
    remotes: &[RemoteIdentity],
    selected: Option<&str>,
) -> Result<Option<String>, String> {
    if let Some(selected) = selected {
        validate_remote_name(selected)?;
        if remotes.iter().any(|remote| remote.name == selected) {
            return Ok(Some(selected.to_string()));
        }
        return Err("the selected Git remote is not configured".into());
    }
    let key = format!("branch.{}.remote", receipt.target_branch);
    if let Ok(configured) = super::git_line(repository, &["config", "--get", &key]) {
        if configured != "." && remotes.iter().any(|remote| remote.name == configured) {
            return Ok(Some(configured));
        }
    }
    if remotes.iter().any(|remote| remote.name == "origin") {
        return Ok(Some("origin".into()));
    }
    if remotes.len() == 1 {
        return Ok(Some(remotes[0].name.clone()));
    }
    Ok(None)
}

fn probe_remote_branch(repository: &Path, remote: &str, branch: &str) -> RemoteProbe {
    let reference = format!("refs/heads/{branch}");
    let output = match Command::new("git")
        .arg("-C")
        .arg(repository)
        .args([
            "ls-remote",
            "--exit-code",
            "--heads",
            "--",
            remote,
            &reference,
        ])
        .env("GIT_TERMINAL_PROMPT", "0")
        .stdin(Stdio::null())
        .output()
    {
        Ok(output) => output,
        Err(_) => return RemoteProbe::Failed(UpstreamBranchStatus::RemoteUnavailable),
    };
    if output.status.success() {
        let line = String::from_utf8_lossy(&output.stdout);
        let mut fields = line.split_whitespace();
        let commit = fields.next().unwrap_or_default();
        let returned_ref = fields.next().unwrap_or_default();
        if super::valid_object_id(commit) && returned_ref == reference && fields.next().is_none() {
            return RemoteProbe::Commit(commit.to_string());
        }
        return RemoteProbe::Failed(UpstreamBranchStatus::RemoteUnavailable);
    }
    if output.status.code() == Some(2) && output.stdout.is_empty() {
        return RemoteProbe::Missing;
    }
    RemoteProbe::Failed(classify_remote_failure(&output))
}

fn classify_remote_failure(output: &Output) -> UpstreamBranchStatus {
    let stderr = String::from_utf8_lossy(&output.stderr).to_ascii_lowercase();
    if [
        "authentication failed",
        "permission denied",
        "could not read username",
        "terminal prompts disabled",
        "publickey",
        "access denied",
    ]
    .iter()
    .any(|pattern| stderr.contains(pattern))
    {
        UpstreamBranchStatus::AuthenticationRequired
    } else {
        UpstreamBranchStatus::RemoteUnavailable
    }
}

fn result_for_probe(
    receipt: &HandoffReceipt,
    remotes: &[RemoteIdentity],
    remote: &RemoteIdentity,
    branch_name: &str,
    probe: RemoteProbe,
    created: bool,
) -> UpstreamBranchResult {
    match probe {
        RemoteProbe::Missing => branch_result(
            receipt,
            remotes,
            Some(&remote.name),
            branch_name,
            UpstreamBranchStatus::Ready,
            created,
            "The protected branch name is available.",
        ),
        RemoteProbe::Commit(commit) if commit == receipt.local_commit_id => branch_result(
            receipt,
            remotes,
            Some(&remote.name),
            branch_name,
            UpstreamBranchStatus::AlreadyPublished,
            false,
            "The remote branch already points to the exact synchronized commit.",
        ),
        RemoteProbe::Commit(_) => branch_result(
            receipt,
            remotes,
            Some(&remote.name),
            branch_name,
            UpstreamBranchStatus::BranchConflict,
            false,
            "That remote branch already exists with different content.",
        ),
        RemoteProbe::Failed(status) => branch_result(
            receipt,
            remotes,
            Some(&remote.name),
            branch_name,
            status,
            false,
            remote_failure_detail(status),
        ),
    }
}

fn branch_result(
    receipt: &HandoffReceipt,
    remotes: &[RemoteIdentity],
    selected_remote: Option<&str>,
    branch_name: &str,
    status: UpstreamBranchStatus,
    created: bool,
    detail: &str,
) -> UpstreamBranchResult {
    UpstreamBranchResult {
        project_id: receipt.project_id.clone(),
        feature_id: receipt.feature_id.clone(),
        repository_path: receipt.destination_repository_path.clone(),
        local_commit_id: receipt.local_commit_id.clone(),
        remotes: remotes
            .iter()
            .map(|remote| UpstreamRemote {
                name: remote.name.clone(),
                display_location: remote.display_location.clone(),
            })
            .collect(),
        selected_remote: selected_remote.map(str::to_string),
        branch_name: branch_name.to_string(),
        status,
        created,
        detail: Some(detail.to_string()),
    }
}

fn remote_failure_detail(status: UpstreamBranchStatus) -> &'static str {
    match status {
        UpstreamBranchStatus::AuthenticationRequired => {
            "System Git could not authenticate with the selected remote."
        }
        _ => "System Git could not reach or inspect the selected remote.",
    }
}

fn validate_remote_name(name: &str) -> Result<(), String> {
    super::validate_text("remote name", name, 256)?;
    if name.starts_with('-')
        || !name
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'.' | b'_' | b'-'))
    {
        return Err("remote name contains unsupported characters".into());
    }
    Ok(())
}

fn validate_protected_branch(repository: &Path, branch: &str) -> Result<(), String> {
    super::validate_text("upstream branch", branch, 256)?;
    if !branch.starts_with(BRANCH_PREFIX) || branch.len() == BRANCH_PREFIX.len() {
        return Err("upstream branch must start with commitarium/".into());
    }
    super::check_branch_name(repository, branch)
}

fn suggested_branch_name(work_order_name: &str, feature_id: &str) -> String {
    let mut slug = String::new();
    let mut separating = false;
    for character in work_order_name.trim().chars() {
        if character.is_ascii_alphanumeric() {
            if separating && !slug.is_empty() {
                slug.push('-');
            }
            slug.push(character.to_ascii_lowercase());
            separating = false;
        } else {
            separating = true;
        }
        if slug.len() >= 96 {
            break;
        }
    }
    while slug.ends_with('-') {
        slug.pop();
    }
    if slug.is_empty() {
        slug = feature_id.to_ascii_lowercase();
    }
    format!("{BRANCH_PREFIX}{slug}")
}

fn display_remote_location(value: &str) -> String {
    if let Ok(url) = reqwest::Url::parse(value) {
        if let Some(host) = url.host_str() {
            let port = url
                .port()
                .map(|port| format!(":{port}"))
                .unwrap_or_default();
            return format!("{}://{}{}{}", url.scheme(), host, port, url.path());
        }
        if url.scheme() == "file" {
            return "local filesystem remote".into();
        }
    }
    if let Some((_, location)) = value.rsplit_once('@') {
        return location.to_string();
    }
    value.to_string()
}

fn fingerprint(value: &str) -> String {
    let digest = Sha256::digest(value.as_bytes());
    digest.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn publication_receipts_path(app: &AppHandle) -> Result<PathBuf, String> {
    let directory = app
        .path()
        .app_data_dir()
        .map_err(|error| format!("resolve app data directory: {error}"))?;
    Ok(directory.join(UPSTREAM_RECEIPTS_FILE))
}

fn load_publication_receipts(path: &Path) -> Result<UpstreamPublicationReceipts, String> {
    match std::fs::read_to_string(path) {
        Ok(contents) => {
            let receipts: UpstreamPublicationReceipts = serde_json::from_str(&contents)
                .map_err(|error| format!("parse upstream publication receipts: {error}"))?;
            if receipts.version != 1 {
                return Err("upstream publication receipt version is unsupported".into());
            }
            for receipt in &receipts.receipts {
                validate_publication_receipt(receipt)?;
            }
            Ok(receipts)
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            Ok(UpstreamPublicationReceipts {
                version: 1,
                receipts: Vec::new(),
            })
        }
        Err(error) => Err(format!("read upstream publication receipts: {error}")),
    }
}

fn validate_publication_receipt(receipt: &UpstreamPublicationReceipt) -> Result<(), String> {
    super::validate_identifier("publication project ID", &receipt.project_id)?;
    super::validate_identifier("publication feature ID", &receipt.feature_id)?;
    super::validate_text(
        "publication repository",
        &receipt.repository_path,
        32 * 1024,
    )?;
    if !super::valid_object_id(&receipt.local_commit_id)
        || !super::valid_object_id(&receipt.remote_fingerprint)
    {
        return Err("upstream publication receipt has an invalid object identity".into());
    }
    validate_remote_name(&receipt.remote_name)?;
    super::validate_text("publication branch", &receipt.branch_name, 256)?;
    if !receipt.branch_name.starts_with(BRANCH_PREFIX)
        || receipt.branch_name.len() == BRANCH_PREFIX.len()
    {
        return Err("upstream publication receipt has an invalid protected branch".into());
    }
    Ok(())
}

fn verify_publication_receipts(
    publications: &UpstreamPublicationReceipts,
    handoff: &HandoffReceipt,
    repository: &Path,
    remote: &RemoteIdentity,
    branch_name: &str,
) -> Result<(), String> {
    let matches: Vec<&UpstreamPublicationReceipt> = publications
        .receipts
        .iter()
        .filter(|receipt| {
            receipt.project_id == handoff.project_id
                && receipt.feature_id == handoff.feature_id
                && receipt.remote_name == remote.name
                && receipt.branch_name == branch_name
        })
        .collect();
    if matches.len() > 1 {
        return Err("more than one upstream receipt exists for this destination".into());
    }
    if let Some(existing) = matches.first() {
        let exact = existing.repository_path == repository.to_string_lossy()
            && existing.local_commit_id == handoff.local_commit_id
            && existing.remote_fingerprint == remote.fingerprint;
        if !exact {
            return Err("the recorded upstream publication disagrees with this destination".into());
        }
    }
    Ok(())
}

fn record_publication(
    path: &Path,
    publications: &mut UpstreamPublicationReceipts,
    handoff: &HandoffReceipt,
    repository: &Path,
    remote: &RemoteIdentity,
    branch_name: &str,
) -> Result<(), String> {
    let receipt = UpstreamPublicationReceipt {
        project_id: handoff.project_id.clone(),
        feature_id: handoff.feature_id.clone(),
        repository_path: repository.to_string_lossy().into_owned(),
        local_commit_id: handoff.local_commit_id.clone(),
        remote_name: remote.name.clone(),
        remote_fingerprint: remote.fingerprint.clone(),
        branch_name: branch_name.to_string(),
    };
    if publications
        .receipts
        .iter()
        .any(|existing| existing == &receipt)
    {
        return Ok(());
    }
    publications.receipts.push(receipt);
    save_publication_receipts(path, publications)
}

fn save_publication_receipts(
    path: &Path,
    receipts: &UpstreamPublicationReceipts,
) -> Result<(), String> {
    let parent = path
        .parent()
        .ok_or("upstream publication receipt path has no parent")?;
    std::fs::create_dir_all(parent)
        .map_err(|error| format!("create upstream receipt directory: {error}"))?;
    let contents = serde_json::to_vec_pretty(receipts)
        .map_err(|error| format!("encode upstream publication receipts: {error}"))?;
    let mut temporary = tempfile::NamedTempFile::new_in(parent)
        .map_err(|error| format!("create temporary upstream receipt: {error}"))?;
    temporary
        .write_all(&contents)
        .and_then(|_| temporary.as_file().sync_all())
        .map_err(|error| format!("write upstream publication receipts: {error}"))?;
    temporary
        .persist(path)
        .map_err(|error| format!("replace upstream publication receipts: {}", error.error))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    struct Fixture {
        _root: tempfile::TempDir,
        repository: PathBuf,
        remote: PathBuf,
        receipts: PathBuf,
        handoff: HandoffReceipt,
    }

    fn fixture() -> Fixture {
        let root = tempfile::tempdir().expect("temp root");
        let repository = root.path().join("repository");
        let remote = root.path().join("upstream.git");
        let receipts = root.path().join("state/upstream-publication-receipts.json");
        std::fs::create_dir(&repository).expect("repository directory");
        command(&repository, &["init", "--initial-branch=main"]);
        command(&repository, &["config", "user.name", "Local User"]);
        command(&repository, &["config", "user.email", "local@example.test"]);
        std::fs::write(repository.join("README.md"), "initial\n").expect("initial file");
        command(&repository, &["add", "README.md"]);
        command(&repository, &["commit", "-m", "Initial"]);
        let base = line(&repository, &["rev-parse", "HEAD"]);
        std::fs::write(repository.join("feature.txt"), "complete\n").expect("feature file");
        command(&repository, &["add", "feature.txt"]);
        command(&repository, &["commit", "-m", "Clean handoff"]);
        let commit = line(&repository, &["rev-parse", "HEAD"]);
        checked(
            Command::new("git")
                .args(["init", "--bare", "--initial-branch=main"])
                .arg(&remote),
        );
        command(
            &repository,
            &["remote", "add", "origin", &remote.to_string_lossy()],
        );
        Fixture {
            _root: root,
            repository: repository.clone(),
            remote,
            receipts,
            handoff: HandoffReceipt {
                project_id: "prj_test".into(),
                feature_id: "fea_test".into(),
                internal_repository_owner: "commitarium".into(),
                internal_repository_name: "project".into(),
                internal_base_commit_id: base.clone(),
                internal_approved_commit_id: commit.clone(),
                internal_merge_commit_id: commit.clone(),
                destination_repository_path: repository.to_string_lossy().into_owned(),
                target_branch: "main".into(),
                local_base_commit_id: base,
                local_commit_id: commit,
                commit_message: "Clean handoff".into(),
                author_name: "Local User".into(),
                author_email: "local@example.test".into(),
            },
        }
    }

    #[test]
    fn suggests_safe_work_order_branch_names() {
        assert_eq!(
            suggested_branch_name("  Update README & docs  ", "fea_test"),
            "commitarium/update-readme-docs"
        );
        assert_eq!(
            suggested_branch_name("Þó", "fea_TEST"),
            "commitarium/fea_test"
        );
    }

    #[test]
    fn previews_publishes_and_retries_exact_new_branch() {
        let fixture = fixture();
        let preview = preview_repository(
            &fixture.repository,
            &fixture.handoff,
            None,
            "commitarium/clean-handoff",
        )
        .expect("preview");
        assert_eq!(preview.status, UpstreamBranchStatus::Ready);
        assert_eq!(preview.selected_remote.as_deref(), Some("origin"));
        let serialized = serde_json::to_value(&preview).expect("serialize preview");
        assert_eq!(serialized["projectId"], "prj_test");
        assert_eq!(serialized["localCommitId"], fixture.handoff.local_commit_id);
        assert_eq!(serialized["status"], "ready");
        assert!(serialized.get("project_id").is_none());

        std::fs::write(fixture.repository.join("uncommitted.txt"), "mine\n")
            .expect("uncommitted file");
        let published = publish_repository(
            &fixture.repository,
            &fixture.receipts,
            &fixture.handoff,
            "origin",
            "commitarium/clean-handoff",
        )
        .expect("publish");
        assert_eq!(published.status, UpstreamBranchStatus::Published);
        assert!(published.created);
        assert_eq!(
            git_dir_line(
                &fixture.remote,
                &["rev-parse", "refs/heads/commitarium/clean-handoff"]
            ),
            fixture.handoff.local_commit_id
        );

        std::fs::remove_file(&fixture.receipts)
            .expect("remove receipt to simulate interrupted receipt write");
        let retried = publish_repository(
            &fixture.repository,
            &fixture.receipts,
            &fixture.handoff,
            "origin",
            "commitarium/clean-handoff",
        )
        .expect("retry");
        assert_eq!(retried.status, UpstreamBranchStatus::AlreadyPublished);
        assert!(!retried.created);
        assert!(fixture.receipts.is_file());
    }

    #[test]
    fn refuses_to_update_an_existing_branch() {
        let fixture = fixture();
        command(
            &fixture.repository,
            &[
                "push",
                "origin",
                &format!(
                    "{}:refs/heads/commitarium/existing",
                    fixture.handoff.local_base_commit_id
                ),
            ],
        );
        let result = publish_repository(
            &fixture.repository,
            &fixture.receipts,
            &fixture.handoff,
            "origin",
            "commitarium/existing",
        )
        .expect("conflict result");
        assert_eq!(result.status, UpstreamBranchStatus::BranchConflict);
        assert_eq!(
            git_dir_line(
                &fixture.remote,
                &["rev-parse", "refs/heads/commitarium/existing"]
            ),
            fixture.handoff.local_base_commit_id
        );
    }

    #[test]
    fn refuses_to_publish_outside_the_protected_namespace() {
        let fixture = fixture();
        let error = publish_repository(
            &fixture.repository,
            &fixture.receipts,
            &fixture.handoff,
            "origin",
            "main",
        )
        .expect_err("default branch must never be a publication target");
        assert!(error.contains("must start with commitarium/"), "{error}");
        assert!(
            git_dir_line_optional(&fixture.remote, &["rev-parse", "refs/heads/main"]).is_none()
        );
    }

    #[test]
    fn redacts_credentials_from_remote_display() {
        assert_eq!(
            display_remote_location("https://user:secret@example.test/owner/repo.git?token=hidden"),
            "https://example.test/owner/repo.git"
        );
        assert_eq!(
            display_remote_location("git@example.test:owner/repo.git"),
            "example.test:owner/repo.git"
        );
    }

    fn command(repository: &Path, args: &[&str]) {
        super::super::run_git_checked(repository, args, &format!("git {}", args.join(" ")))
            .unwrap_or_else(|error| panic!("{error}"));
    }

    fn line(repository: &Path, args: &[&str]) -> String {
        super::super::git_line(repository, args).unwrap_or_else(|error| panic!("{error}"))
    }

    fn git_dir_line(repository: &Path, args: &[&str]) -> String {
        super::super::git_dir_line(repository, args).unwrap_or_else(|error| panic!("{error}"))
    }

    fn git_dir_line_optional(repository: &Path, args: &[&str]) -> Option<String> {
        super::super::git_dir_line(repository, args).ok()
    }

    fn checked(command: &mut Command) {
        super::super::run_checked(command, "test Git command")
            .unwrap_or_else(|error| panic!("{error}"));
    }
}
