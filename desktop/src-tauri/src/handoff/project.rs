//! Project-level synchronization and upstream publication.
//!
//! The coordinator owns the canonical Forgejo identities. This trusted desktop
//! module owns host paths, system Git credentials, and per-destination
//! watermarks. Each new synchronization applies only the canonical change since
//! the destination's previous watermark, which prevents earlier work orders
//! from being replayed.

use super::*;
use crate::import::ProjectSource;
use serde::{Deserialize, Serialize};
use std::io::Write;
use std::process::Stdio;

const PROJECT_RECEIPTS_FILE: &str = "project-handoff-receipts.json";
const PROJECT_UPSTREAM_RECEIPTS_FILE: &str = "project-upstream-receipts.json";
const PROJECT_SETUP_RECEIPTS_FILE: &str = "project-workspace-setup-receipts.json";

#[derive(Clone, Debug, Deserialize)]
struct ProjectHandoff {
    project_id: String,
    source: ProjectHandoffSource,
    completed_features: Vec<ProjectHandoffFeature>,
}

#[derive(Clone, Debug, Deserialize)]
struct ProjectHandoffSource {
    repository: HandoffRepository,
    default_branch: String,
    head_commit_id: String,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
#[serde(rename_all(deserialize = "snake_case", serialize = "camelCase"))]
struct ProjectHandoffFeature {
    feature_id: String,
    title: String,
    base_commit_id: String,
    merge_commit_id: String,
    merged_at: String,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
enum ProjectReceiptStatus {
    Prepared,
    Completed,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct ProjectReceipt {
    project_id: String,
    source_type: String,
    source_path: String,
    internal_repository_owner: String,
    internal_repository_name: String,
    internal_base_commit_id: String,
    internal_head_commit_id: String,
    base_tree_id: String,
    head_tree_id: String,
    target_branch: Option<String>,
    local_base_commit_id: Option<String>,
    local_commit_id: Option<String>,
    commit_message: Option<String>,
    status: ProjectReceiptStatus,
}

#[derive(Debug, Deserialize, Serialize)]
struct ProjectReceipts {
    version: u32,
    receipts: Vec<ProjectReceipt>,
}

impl Default for ProjectReceipts {
    fn default() -> Self {
        Self {
            version: 1,
            receipts: Vec::new(),
        }
    }
}

#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
struct ProjectUpstreamReceipt {
    project_id: String,
    internal_head_commit_id: String,
    repository_path: String,
    local_commit_id: String,
    remote_name: String,
    remote_fingerprint: String,
    branch_name: String,
}

#[derive(Debug, Deserialize, Serialize)]
struct ProjectUpstreamReceipts {
    version: u32,
    receipts: Vec<ProjectUpstreamReceipt>,
}

impl Default for ProjectUpstreamReceipts {
    fn default() -> Self {
        Self {
            version: 1,
            receipts: Vec::new(),
        }
    }
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
enum ProjectSetupStatus {
    Prepared,
    Installed,
    Completed,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct ProjectSetupReceipt {
    idempotency_key: String,
    project_id: String,
    destination_path: String,
    staging_path: String,
    default_branch: String,
    canonical_commit_id: String,
    canonical_tree_id: String,
    local_commit_id: String,
    commit_message: String,
    author_name: String,
    author_email: String,
    status: ProjectSetupStatus,
}

#[derive(Debug, Deserialize, Serialize)]
struct ProjectSetupReceipts {
    version: u32,
    receipts: Vec<ProjectSetupReceipt>,
}

impl Default for ProjectSetupReceipts {
    fn default() -> Self {
        Self {
            version: 1,
            receipts: Vec::new(),
        }
    }
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ProjectSyncState {
    project_id: String,
    source: Option<ProjectSourceState>,
    canonical: ProjectCanonicalState,
    local: ProjectTargetState,
    upstreams: Vec<ProjectUpstreamState>,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
struct ProjectSourceState {
    source_type: String,
    path: String,
    created_by_commitarium: bool,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
struct ProjectCanonicalState {
    default_branch: String,
    head_commit_id: String,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
struct ProjectTargetState {
    watermark_commit_id: Option<String>,
    local_commit_id: Option<String>,
    unsynced_features: Vec<ProjectHandoffFeature>,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
struct ProjectUpstreamState {
    remote_name: String,
    display_location: String,
    branch_name: Option<String>,
    watermark_commit_id: Option<String>,
    unsynced_features: Vec<ProjectHandoffFeature>,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ProjectSynchronizeResult {
    project_id: String,
    source_type: String,
    source_path: String,
    canonical_commit_id: String,
    target_branch: Option<String>,
    local_commit_id: Option<String>,
    result_tree_id: String,
    created: bool,
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GitIdentityState {
    name: Option<String>,
    email: Option<String>,
}

#[derive(Debug)]
pub(crate) struct NewRemoteContext {
    pub(crate) repository_path: PathBuf,
    pub(crate) default_branch: String,
    pub(crate) canonical_commit_id: String,
    pub(crate) local_commit_id: String,
}

pub(crate) async fn new_remote_context(
    app: &AppHandle,
    project_id: &str,
) -> Result<NewRemoteContext, String> {
    validate_identifier("project ID", project_id)?;
    let handoff = fetch_project_handoff(project_id).await?;
    validate_project_handoff(&handoff, project_id)?;
    let source = crate::import::get_project_source_record(app, project_id)?
        .ok_or("this project has no trusted local source mapping")?;
    let receipts_path = project_receipts_path(app)?;
    let (repository, receipt) = project_publication_source(&source, &receipts_path, &handoff)?;
    Ok(NewRemoteContext {
        repository_path: repository,
        default_branch: receipt
            .target_branch
            .ok_or("the local project sync has no target branch")?,
        canonical_commit_id: receipt.internal_head_commit_id,
        local_commit_id: receipt
            .local_commit_id
            .ok_or("the local project sync has no commit")?,
    })
}

/// Read the effective host Git identity without modifying Git configuration.
#[tauri::command]
pub async fn get_git_identity(parent_path: Option<String>) -> Result<GitIdentityState, String> {
    tokio::task::spawn_blocking(move || {
        let directory = parent_path
            .as_deref()
            .map(Path::new)
            .filter(|path| path.is_dir());
        let read = |key: &str| {
            let mut command = Command::new("git");
            if let Some(directory) = directory {
                command.arg("-C").arg(directory);
            }
            command
                .args(["config", "--get", key])
                .output()
                .ok()
                .filter(|output| output.status.success())
                .and_then(|output| {
                    let value = String::from_utf8_lossy(&output.stdout).trim().to_string();
                    (!value.is_empty()).then_some(value)
                })
        };
        Ok(GitIdentityState {
            name: read("user.name"),
            email: read("user.email"),
        })
    })
    .await
    .map_err(|error| format!("Git identity task failed: {error}"))?
}

/// Materialize a coordinator-created project as a new, clean local Git
/// repository. The internal Forgejo history is deliberately replaced by one
/// user-authored root commit before the destination is installed.
#[tauri::command]
#[allow(clippy::too_many_arguments)]
pub async fn initialize_project_local_repository(
    app: AppHandle,
    project_id: String,
    parent_path: String,
    folder_name: String,
    commit_message: String,
    author_name: String,
    author_email: String,
    idempotency_key: String,
) -> Result<ProjectSynchronizeResult, String> {
    validate_identifier("project ID", &project_id)?;
    validate_text("commit message", &commit_message, 4096)?;
    validate_text("Idempotency-Key", &idempotency_key, 512)?;
    validate_identity("Git author name", &author_name)?;
    validate_identity("Git author email", &author_email)?;
    validate_folder_name(&folder_name)?;
    let handoff = fetch_project_handoff(&project_id).await?;
    validate_project_handoff(&handoff, &project_id)?;
    let token = forgejo_token_path()
        .ok()
        .and_then(|path| std::fs::read_to_string(path).ok())
        .map(|value| value.trim().to_string())
        .filter(|value| !value.is_empty());
    let internal_url = internal_repository_url(&handoff.source.repository)?;
    let receipts_path = project_receipts_path(&app)?;
    let setup_path = project_setup_receipts_path(&app)?;
    let app_for_task = app.clone();

    tokio::task::spawn_blocking(move || {
        let _guard = HANDOFF_LOCK
            .lock()
            .map_err(|_| "the local handoff lock is unavailable".to_string())?;
        initialize_project_repository(
            &app_for_task,
            &handoff,
            Path::new(&parent_path),
            &folder_name,
            &commit_message,
            &GitIdentity {
                name: author_name,
                email: author_email,
            },
            &idempotency_key,
            &internal_url,
            token.as_deref(),
            &receipts_path,
            &setup_path,
        )
    })
    .await
    .map_err(|error| format!("project workspace setup task failed: {error}"))?
}

#[allow(clippy::too_many_arguments)]
fn initialize_project_repository(
    app: &AppHandle,
    handoff: &ProjectHandoff,
    parent_path: &Path,
    folder_name: &str,
    commit_message: &str,
    identity: &GitIdentity,
    idempotency_key: &str,
    internal_url: &str,
    token: Option<&str>,
    receipts_path: &Path,
    setup_path: &Path,
) -> Result<ProjectSynchronizeResult, String> {
    let parent = std::fs::canonicalize(parent_path)
        .map_err(|error| format!("resolve workspace parent folder: {error}"))?;
    if !parent.is_dir() {
        return Err("the selected workspace parent is not a folder".into());
    }
    let destination = parent.join(folder_name);
    let destination_text = destination.to_string_lossy().into_owned();
    let mut setups = load_project_setup_receipts(setup_path)?;

    if let Some(index) = setups
        .receipts
        .iter()
        .position(|receipt| receipt.idempotency_key == idempotency_key)
    {
        let receipt = &setups.receipts[index];
        if receipt.project_id != handoff.project_id
            || receipt.destination_path != destination_text
            || receipt.commit_message != commit_message
            || receipt.author_name != identity.name
            || receipt.author_email != identity.email
        {
            return Err(
                "this Idempotency-Key was already used for different workspace setup parameters"
                    .into(),
            );
        }
        if receipt.status == ProjectSetupStatus::Completed {
            verify_bootstrap_repository(Path::new(&receipt.destination_path), receipt)?;
            let source = crate::import::get_project_source_record(app, &handoff.project_id)?
                .ok_or("the completed workspace setup has no trusted source mapping")?;
            if source.source_type != "git" || source.path != receipt.destination_path {
                return Err(
                    "the completed workspace setup disagrees with the trusted source mapping"
                        .into(),
                );
            }
            return setup_result(receipt, false);
        }
        resume_project_setup(app, handoff, receipts_path, setup_path, &mut setups, index)?;
        return setup_result(&setups.receipts[index], false);
    }

    if let Some(source) = crate::import::get_project_source_record(app, &handoff.project_id)? {
        return Err(format!(
            "this project already has a trusted local source mapping at {}",
            source.path
        ));
    }
    require_available_destination(&destination)?;

    let material =
        prepare_project_material(internal_url, handoff, &handoff.source.head_commit_id, token)?;
    let temporary = tempfile::Builder::new()
        .prefix(".commitarium-setup-")
        .tempdir_in(&parent)
        .map_err(|error| format!("create workspace staging folder: {error}"))?;
    let staging = temporary.keep();
    let local_commit = match create_bootstrap_repository(
        &staging,
        &material.repository,
        handoff,
        commit_message,
        identity,
        &material.head_tree,
    ) {
        Ok(commit) => commit,
        Err(error) => {
            let _ = std::fs::remove_dir_all(&staging);
            return Err(error);
        }
    };

    setups.receipts.push(ProjectSetupReceipt {
        idempotency_key: idempotency_key.to_string(),
        project_id: handoff.project_id.clone(),
        destination_path: destination_text,
        staging_path: staging.to_string_lossy().into_owned(),
        default_branch: handoff.source.default_branch.clone(),
        canonical_commit_id: handoff.source.head_commit_id.clone(),
        canonical_tree_id: material.head_tree,
        local_commit_id: local_commit,
        commit_message: commit_message.to_string(),
        author_name: identity.name.clone(),
        author_email: identity.email.clone(),
        status: ProjectSetupStatus::Prepared,
    });
    let index = setups.receipts.len() - 1;
    save_project_setup_receipts(setup_path, &setups)?;
    resume_project_setup(app, handoff, receipts_path, setup_path, &mut setups, index)?;
    setup_result(&setups.receipts[index], true)
}

fn resume_project_setup(
    app: &AppHandle,
    handoff: &ProjectHandoff,
    receipts_path: &Path,
    setup_path: &Path,
    setups: &mut ProjectSetupReceipts,
    index: usize,
) -> Result<(), String> {
    let receipt = setups.receipts[index].clone();
    let destination = Path::new(&receipt.destination_path);
    let staging = Path::new(&receipt.staging_path);

    if receipt.status == ProjectSetupStatus::Prepared {
        if verify_bootstrap_repository(destination, &receipt).is_err() {
            verify_bootstrap_repository(staging, &receipt).map_err(|_| {
                "the prepared workspace is missing or changed; no local files were overwritten"
                    .to_string()
            })?;
            require_available_destination(destination)?;
            if destination.exists() {
                std::fs::remove_dir(destination)
                    .map_err(|error| format!("remove selected empty destination: {error}"))?;
            }
            std::fs::rename(staging, destination)
                .map_err(|error| format!("install prepared local workspace: {error}"))?;
        }
        verify_bootstrap_repository(destination, &receipt)?;
        setups.receipts[index].status = ProjectSetupStatus::Installed;
        save_project_setup_receipts(setup_path, setups)?;
    }

    verify_bootstrap_repository(destination, &setups.receipts[index])?;
    record_bootstrap_sync_receipt(receipts_path, handoff, &setups.receipts[index])?;
    crate::import::record_source(
        app,
        &handoff.project_id,
        ProjectSource {
            path: std::fs::canonicalize(destination)
                .map_err(|error| format!("resolve installed local workspace: {error}"))?
                .to_string_lossy()
                .into_owned(),
            source_type: "git".to_string(),
            import_commit_id: Some(receipt.canonical_commit_id.clone()),
            created_by_commitarium: true,
        },
    )?;
    setups.receipts[index].status = ProjectSetupStatus::Completed;
    save_project_setup_receipts(setup_path, setups)
}

fn require_available_destination(destination: &Path) -> Result<(), String> {
    let metadata = match std::fs::symlink_metadata(destination) {
        Ok(metadata) => metadata,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(error) => return Err(format!("inspect local workspace destination: {error}")),
    };
    if metadata.file_type().is_symlink() || !metadata.is_dir() {
        return Err(
            "the local workspace destination already exists and is not an empty folder".into(),
        );
    }
    let mut entries = std::fs::read_dir(destination)
        .map_err(|error| format!("inspect local workspace destination: {error}"))?;
    if entries.next().is_some() {
        return Err("the local workspace destination is not empty; no files were changed".into());
    }
    Ok(())
}

fn create_bootstrap_repository(
    staging: &Path,
    internal: &Path,
    handoff: &ProjectHandoff,
    commit_message: &str,
    identity: &GitIdentity,
    expected_tree: &str,
) -> Result<String, String> {
    run_checked(
        Command::new("git")
            .args(["init", "--quiet", "--initial-branch"])
            .arg(&handoff.source.default_branch)
            .arg("--")
            .arg(staging),
        "initialize local workspace repository",
    )?;
    check_branch_name(staging, &handoff.source.default_branch)?;
    let bootstrap_ref = "refs/commitarium/bootstrap";
    let refspec = format!("+{}:{bootstrap_ref}", handoff.source.head_commit_id);
    run_checked(
        Command::new("git")
            .arg("-C")
            .arg(staging)
            .args([
                "fetch",
                "--quiet",
                "--no-tags",
                "--no-write-fetch-head",
                "--",
            ])
            .arg(internal)
            .arg(refspec),
        "load canonical project tree into local workspace",
    )?;
    let tree = git_line(
        staging,
        &["rev-parse", &format!("{bootstrap_ref}^{{tree}}")],
    )?;
    if tree != expected_tree {
        return Err("the fetched canonical tree changed while the workspace was prepared".into());
    }

    let mut child = Command::new("git")
        .arg("-C")
        .arg(staging)
        .args(["commit-tree", &tree, "-F", "-"])
        .env("GIT_AUTHOR_NAME", &identity.name)
        .env("GIT_AUTHOR_EMAIL", &identity.email)
        .env("GIT_COMMITTER_NAME", &identity.name)
        .env("GIT_COMMITTER_EMAIL", &identity.email)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|error| format!("create local workspace root commit: {error}"))?;
    child
        .stdin
        .as_mut()
        .ok_or("Git did not open stdin for the workspace commit")?
        .write_all(commit_message.as_bytes())
        .map_err(|error| format!("write local workspace commit message: {error}"))?;
    let output = child
        .wait_with_output()
        .map_err(|error| format!("wait for local workspace root commit: {error}"))?;
    if !output.status.success() {
        return Err(format!(
            "create local workspace root commit: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    let commit = String::from_utf8_lossy(&output.stdout).trim().to_string();
    if !valid_object_id(&commit) {
        return Err("Git returned an invalid local workspace commit".into());
    }
    let branch_ref = format!("refs/heads/{}", handoff.source.default_branch);
    run_git_checked(
        staging,
        &["update-ref", &branch_ref, &commit],
        "install local workspace root commit",
    )?;
    run_git_checked(
        staging,
        &["symbolic-ref", "HEAD", &branch_ref],
        "select local workspace default branch",
    )?;
    run_git_checked(
        staging,
        &["reset", "--quiet", "--hard", &commit],
        "check out local workspace",
    )?;
    run_git_checked(
        staging,
        &["update-ref", "-d", bootstrap_ref],
        "forget internal bootstrap history",
    )?;
    run_git_checked(
        staging,
        &["reflog", "expire", "--expire=now", "--all"],
        "expire internal bootstrap reflog",
    )?;
    run_git_checked(
        staging,
        &["gc", "--quiet", "--prune=now"],
        "prune internal bootstrap history",
    )?;
    let commits = git_line(staging, &["rev-list", "--count", "--all"])?;
    if commits != "1" {
        return Err("the local workspace contains unexpected Git history".into());
    }
    let committed_tree = git_line(staging, &["rev-parse", "HEAD^{tree}"])?;
    if committed_tree != expected_tree {
        return Err("the local workspace tree differs from the canonical project".into());
    }
    let status = git_line_allow_empty(
        staging,
        &["status", "--porcelain=v1", "--untracked-files=all"],
    )?;
    if !status.is_empty() {
        return Err("the prepared local workspace is not clean".into());
    }
    Ok(commit)
}

fn verify_bootstrap_repository(
    destination: &Path,
    receipt: &ProjectSetupReceipt,
) -> Result<(), String> {
    if !destination.is_dir() || !crate::import::is_repo_root(destination) {
        return Err("the prepared local workspace is not a Git repository".into());
    }
    let branch = git_line(destination, &["symbolic-ref", "--quiet", "--short", "HEAD"])?;
    let commit = git_line(destination, &["rev-parse", "HEAD"])?;
    let tree = git_line(destination, &["rev-parse", "HEAD^{tree}"])?;
    let status = git_line_allow_empty(
        destination,
        &["status", "--porcelain=v1", "--untracked-files=all"],
    )?;
    if branch != receipt.default_branch
        || commit != receipt.local_commit_id
        || tree != receipt.canonical_tree_id
        || !status.is_empty()
    {
        return Err("the prepared local workspace changed and was not adopted".into());
    }
    Ok(())
}

fn record_bootstrap_sync_receipt(
    path: &Path,
    handoff: &ProjectHandoff,
    setup: &ProjectSetupReceipt,
) -> Result<(), String> {
    let mut receipts = load_project_receipts(path)?;
    if receipts.receipts.iter().any(|receipt| {
        receipt.project_id == setup.project_id
            && receipt.source_path == setup.destination_path
            && receipt.internal_head_commit_id == setup.canonical_commit_id
            && receipt.local_commit_id.as_deref() == Some(&setup.local_commit_id)
    }) {
        return Ok(());
    }
    receipts.receipts.push(ProjectReceipt {
        project_id: setup.project_id.clone(),
        source_type: "git".to_string(),
        source_path: setup.destination_path.clone(),
        internal_repository_owner: handoff.source.repository.owner.clone(),
        internal_repository_name: handoff.source.repository.name.clone(),
        internal_base_commit_id: setup.canonical_commit_id.clone(),
        internal_head_commit_id: setup.canonical_commit_id.clone(),
        base_tree_id: setup.canonical_tree_id.clone(),
        head_tree_id: setup.canonical_tree_id.clone(),
        target_branch: Some(setup.default_branch.clone()),
        local_base_commit_id: None,
        local_commit_id: Some(setup.local_commit_id.clone()),
        commit_message: Some(setup.commit_message.clone()),
        status: ProjectReceiptStatus::Completed,
    });
    save_project_receipts(path, &receipts)
}

fn setup_result(
    receipt: &ProjectSetupReceipt,
    created: bool,
) -> Result<ProjectSynchronizeResult, String> {
    if receipt.status != ProjectSetupStatus::Completed {
        return Err("the local workspace setup did not complete".into());
    }
    Ok(ProjectSynchronizeResult {
        project_id: receipt.project_id.clone(),
        source_type: "git".to_string(),
        source_path: receipt.destination_path.clone(),
        canonical_commit_id: receipt.canonical_commit_id.clone(),
        target_branch: Some(receipt.default_branch.clone()),
        local_commit_id: Some(receipt.local_commit_id.clone()),
        result_tree_id: receipt.canonical_tree_id.clone(),
        created,
    })
}

fn validate_folder_name(name: &str) -> Result<(), String> {
    validate_text("workspace folder name", name, 255)?;
    let mut components = Path::new(name).components();
    if !matches!(components.next(), Some(std::path::Component::Normal(_)))
        || components.next().is_some()
        || name.contains(['/', '\\'])
    {
        return Err("workspace folder name must be one ordinary path component".into());
    }
    Ok(())
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ProjectUpstreamResult {
    project_id: String,
    repository_path: String,
    canonical_commit_id: String,
    local_commit_id: String,
    remotes: Vec<super::upstream::UpstreamRemote>,
    selected_remote: Option<String>,
    branch_name: String,
    status: super::upstream::UpstreamBranchStatus,
    created: bool,
    detail: Option<String>,
}

struct ProjectMaterial {
    _temporary: tempfile::TempDir,
    repository: PathBuf,
    patch: Option<PathBuf>,
    base_tree: String,
    head_tree: String,
}

#[tauri::command]
pub async fn get_project_sync_state(
    app: AppHandle,
    project_id: String,
) -> Result<ProjectSyncState, String> {
    validate_identifier("project ID", &project_id)?;
    let handoff = fetch_project_handoff(&project_id).await?;
    validate_project_handoff(&handoff, &project_id)?;
    let source = crate::import::get_project_source_record(&app, &project_id)?;
    let receipts_path = project_receipts_path(&app)?;
    let upstream_path = project_upstream_receipts_path(&app)?;
    let remote_setup_path = crate::git_providers::remote_receipts_path(&app)?;
    let feature_receipts_path = receipts_path_for_features(&app)?;
    let folder_receipts_path = super::plain_folder::folder_receipts_path(&app)?;
    tokio::task::spawn_blocking(move || {
        build_sync_state(
            &handoff,
            source.as_ref(),
            &receipts_path,
            &feature_receipts_path,
            &folder_receipts_path,
            &upstream_path,
            &remote_setup_path,
        )
    })
    .await
    .map_err(|error| format!("project sync-state task failed: {error}"))?
}

#[tauri::command]
pub async fn synchronize_project_locally(
    app: AppHandle,
    project_id: String,
    commit_message: String,
) -> Result<ProjectSynchronizeResult, String> {
    validate_identifier("project ID", &project_id)?;
    validate_text("commit message", &commit_message, 4096)?;
    let handoff = fetch_project_handoff(&project_id).await?;
    validate_project_handoff(&handoff, &project_id)?;
    let source = crate::import::get_project_source_record(&app, &project_id)?
        .ok_or("this project has no trusted local source mapping")?;
    let receipts_path = project_receipts_path(&app)?;
    let feature_receipts_path = receipts_path_for_features(&app)?;
    let folder_receipts_path = super::plain_folder::folder_receipts_path(&app)?;
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
        synchronize_project(
            &source,
            &receipts_path,
            &feature_receipts_path,
            &folder_receipts_path,
            &handoff,
            &commit_message,
            &internal_url,
            token.as_deref(),
        )
    })
    .await
    .map_err(|error| format!("project synchronization task failed: {error}"))?
}

#[tauri::command]
pub async fn preview_project_upstream_branch(
    app: AppHandle,
    project_id: String,
    remote_name: Option<String>,
    branch_name: Option<String>,
) -> Result<ProjectUpstreamResult, String> {
    validate_identifier("project ID", &project_id)?;
    let handoff = fetch_project_handoff(&project_id).await?;
    validate_project_handoff(&handoff, &project_id)?;
    let source = crate::import::get_project_source_record(&app, &project_id)?
        .ok_or("this project has no trusted local source mapping")?;
    let receipts_path = project_receipts_path(&app)?;
    let selected_branch = branch_name.unwrap_or_else(|| {
        format!(
            "commitarium/project-{}",
            &handoff.source.head_commit_id[..12]
        )
    });
    tokio::task::spawn_blocking(move || {
        let _guard = HANDOFF_LOCK
            .lock()
            .map_err(|_| "the local handoff lock is unavailable".to_string())?;
        preview_project_upstream(
            &source,
            &receipts_path,
            &handoff,
            remote_name.as_deref(),
            &selected_branch,
        )
    })
    .await
    .map_err(|error| format!("project upstream preview task failed: {error}"))?
}

#[tauri::command]
pub async fn publish_project_upstream_branch(
    app: AppHandle,
    project_id: String,
    remote_name: String,
    branch_name: String,
) -> Result<ProjectUpstreamResult, String> {
    validate_identifier("project ID", &project_id)?;
    super::upstream::validate_remote_name(&remote_name)?;
    let handoff = fetch_project_handoff(&project_id).await?;
    validate_project_handoff(&handoff, &project_id)?;
    let source = crate::import::get_project_source_record(&app, &project_id)?
        .ok_or("this project has no trusted local source mapping")?;
    let receipts_path = project_receipts_path(&app)?;
    let upstream_path = project_upstream_receipts_path(&app)?;
    tokio::task::spawn_blocking(move || {
        let _guard = HANDOFF_LOCK
            .lock()
            .map_err(|_| "the local handoff lock is unavailable".to_string())?;
        publish_project_upstream(
            &source,
            &receipts_path,
            &upstream_path,
            &handoff,
            &remote_name,
            &branch_name,
        )
    })
    .await
    .map_err(|error| format!("project upstream publication task failed: {error}"))?
}

async fn fetch_project_handoff(project_id: &str) -> Result<ProjectHandoff, String> {
    let response = reqwest::Client::new()
        .get(format!(
            "{COORDINATOR_BASE}/api/v1/projects/{project_id}/handoff"
        ))
        .send()
        .await
        .map_err(|error| format!("request project handoff from coordinator: {error}"))?;
    let status = response.status();
    let body = response.text().await.unwrap_or_default();
    if !status.is_success() {
        if let Ok(value) = serde_json::from_str::<serde_json::Value>(&body) {
            if let Some(message) = value.pointer("/error/message").and_then(|v| v.as_str()) {
                return Err(message.to_string());
            }
        }
        return Err(format!(
            "project handoff request failed (HTTP {})",
            status.as_u16()
        ));
    }
    serde_json::from_str(&body).map_err(|error| format!("parse project handoff: {error}"))
}

fn validate_project_handoff(handoff: &ProjectHandoff, project_id: &str) -> Result<(), String> {
    if handoff.project_id != project_id {
        return Err("coordinator returned a handoff for another project".into());
    }
    validate_identifier("handoff project ID", &handoff.project_id)?;
    validate_coordinate("repository owner", &handoff.source.repository.owner)?;
    validate_coordinate("repository name", &handoff.source.repository.name)?;
    validate_text("default branch", &handoff.source.default_branch, 256)?;
    if !valid_object_id(&handoff.source.head_commit_id) {
        return Err("canonical head is not a valid Git object ID".into());
    }
    for (index, item) in handoff.completed_features.iter().enumerate() {
        validate_identifier("completed feature ID", &item.feature_id)?;
        validate_text("completed feature title", &item.title, 512)?;
        validate_text("completed feature merge time", &item.merged_at, 128)?;
        if !valid_object_id(&item.base_commit_id) || !valid_object_id(&item.merge_commit_id) {
            return Err("completed feature has an invalid Git object ID".into());
        }
        if index > 0 && item.base_commit_id != handoff.completed_features[index - 1].merge_commit_id
        {
            return Err(
                "completed work orders do not form one continuous canonical history".into(),
            );
        }
    }
    if let Some(last) = handoff.completed_features.last() {
        if last.merge_commit_id != handoff.source.head_commit_id {
            return Err(
                "the canonical default branch contains work outside the recorded completed work orders; user review is required"
                    .into(),
            );
        }
    }
    Ok(())
}

fn build_sync_state(
    handoff: &ProjectHandoff,
    source: Option<&ProjectSource>,
    receipts_path: &Path,
    feature_receipts_path: &Path,
    folder_receipts_path: &Path,
    upstream_path: &Path,
    remote_setup_path: &Path,
) -> Result<ProjectSyncState, String> {
    let receipts = load_project_receipts(receipts_path)?;
    let local_receipt =
        source.and_then(|source| latest_completed_project_receipt(&receipts, handoff, source));
    let local_watermark = local_receipt
        .map(|receipt| receipt.internal_head_commit_id.clone())
        .or(legacy_feature_watermark(
            handoff,
            source,
            feature_receipts_path,
            folder_receipts_path,
        )?)
        .or_else(|| source.and_then(|value| value.import_commit_id.clone()))
        .or_else(|| {
            source.and_then(|_| {
                handoff
                    .completed_features
                    .first()
                    .map(|item| item.base_commit_id.clone())
            })
        });
    let local = ProjectTargetState {
        watermark_commit_id: local_watermark.clone(),
        local_commit_id: local_receipt.and_then(|receipt| receipt.local_commit_id.clone()),
        unsynced_features: features_after(&handoff.completed_features, local_watermark.as_deref()),
    };

    let mut upstreams = Vec::new();
    if let Some(source) = source.filter(|source| source.source_type == "git") {
        let repository = Path::new(&source.path);
        if repository.is_dir() && crate::import::is_repo_root(repository) {
            let publications = load_project_upstream_receipts(upstream_path)?;
            let created = crate::git_providers::created_remote_watermarks(
                remote_setup_path,
                &handoff.project_id,
                &repository.to_string_lossy(),
            )?;
            for remote in super::upstream::configured_remotes(repository)? {
                let publication = publications.receipts.iter().rev().find(|receipt| {
                    receipt.project_id == handoff.project_id
                        && receipt.repository_path == repository.to_string_lossy()
                        && receipt.remote_name == remote.name
                        && receipt.remote_fingerprint == remote.fingerprint
                });
                let created_publication = created
                    .iter()
                    .find(|receipt| receipt.remote_name == remote.name);
                let watermark = publication
                    .map(|receipt| receipt.internal_head_commit_id.clone())
                    .or_else(|| {
                        created_publication.map(|receipt| receipt.canonical_commit_id.clone())
                    });
                upstreams.push(ProjectUpstreamState {
                    remote_name: remote.name,
                    display_location: remote.display_location,
                    branch_name: publication
                        .map(|receipt| receipt.branch_name.clone())
                        .or_else(|| {
                            created_publication.map(|receipt| receipt.default_branch.clone())
                        }),
                    watermark_commit_id: watermark.clone(),
                    unsynced_features: features_after(
                        &handoff.completed_features,
                        watermark.as_deref(),
                    ),
                });
            }
        }
    }
    Ok(ProjectSyncState {
        project_id: handoff.project_id.clone(),
        source: source.map(|source| ProjectSourceState {
            source_type: source.source_type.clone(),
            path: source.path.clone(),
            created_by_commitarium: source.created_by_commitarium,
        }),
        canonical: ProjectCanonicalState {
            default_branch: handoff.source.default_branch.clone(),
            head_commit_id: handoff.source.head_commit_id.clone(),
        },
        local,
        upstreams,
    })
}

fn features_after(
    completed: &[ProjectHandoffFeature],
    watermark: Option<&str>,
) -> Vec<ProjectHandoffFeature> {
    let start = watermark
        .and_then(|commit| {
            completed
                .iter()
                .position(|item| item.merge_commit_id == commit)
                .map(|index| index + 1)
        })
        .unwrap_or(0);
    completed[start..].to_vec()
}

fn legacy_feature_watermark(
    handoff: &ProjectHandoff,
    source: Option<&ProjectSource>,
    feature_receipts_path: &Path,
    folder_receipts_path: &Path,
) -> Result<Option<String>, String> {
    let Some(source) = source else {
        return Ok(None);
    };
    let canonical = match std::fs::canonicalize(&source.path) {
        Ok(path) => path,
        Err(_) => return Ok(None),
    };
    if source.source_type == "git" {
        let receipts = super::load_receipts(feature_receipts_path)?;
        for item in handoff.completed_features.iter().rev() {
            if receipts.receipts.iter().rev().any(|receipt| {
                receipt.project_id == handoff.project_id
                    && receipt.feature_id == item.feature_id
                    && receipt.internal_repository_owner == handoff.source.repository.owner
                    && receipt.internal_repository_name == handoff.source.repository.name
                    && receipt.internal_merge_commit_id == item.merge_commit_id
                    && receipt.destination_repository_path == canonical.to_string_lossy()
            }) {
                return Ok(Some(item.merge_commit_id.clone()));
            }
        }
    } else if source.source_type == "plain_folder" {
        for item in handoff.completed_features.iter().rev() {
            if super::plain_folder::completed_feature_watermark(
                folder_receipts_path,
                &handoff.project_id,
                &item.feature_id,
                &canonical,
                &item.merge_commit_id,
            )?
            .is_some()
            {
                return Ok(Some(item.merge_commit_id.clone()));
            }
        }
    }
    Ok(None)
}

// These values describe one verified synchronization operation; keeping them
// explicit avoids hiding security-relevant paths and revisions in a loose bag.
#[allow(clippy::too_many_arguments)]
fn synchronize_project(
    source: &ProjectSource,
    receipts_path: &Path,
    feature_receipts_path: &Path,
    folder_receipts_path: &Path,
    handoff: &ProjectHandoff,
    commit_message: &str,
    internal_url: &str,
    token: Option<&str>,
) -> Result<ProjectSynchronizeResult, String> {
    let source_path = std::fs::canonicalize(&source.path)
        .map_err(|error| format!("resolve imported project source: {error}"))?;
    let mut receipts = load_project_receipts(receipts_path)?;
    let latest = latest_completed_project_receipt(&receipts, handoff, source).cloned();
    if let Some(receipt) = latest.as_ref().filter(|receipt| {
        receipt.internal_head_commit_id == handoff.source.head_commit_id
            && receipt.status == ProjectReceiptStatus::Completed
    }) {
        verify_completed_project_receipt(receipt, &source_path, handoff)?;
        return Ok(project_result(receipt, false));
    }
    let base_commit = latest
        .as_ref()
        .map(|receipt| receipt.internal_head_commit_id.clone())
        .or(legacy_feature_watermark(
            handoff,
            Some(source),
            feature_receipts_path,
            folder_receipts_path,
        )?)
        .or_else(|| source.import_commit_id.clone())
        .or_else(|| {
            handoff
                .completed_features
                .first()
                .map(|item| item.base_commit_id.clone())
        })
        .unwrap_or_else(|| handoff.source.head_commit_id.clone());
    if !valid_object_id(&base_commit) {
        return Err("the trusted project import base is not a valid Git object ID".into());
    }
    let material = prepare_project_material(internal_url, handoff, &base_commit, token)?;
    if source.source_type == "git" {
        synchronize_project_repository(
            &source_path,
            receipts_path,
            &mut receipts,
            handoff,
            &base_commit,
            &material,
            commit_message,
        )
    } else if source.source_type == "plain_folder" {
        synchronize_project_folder(
            &source_path,
            receipts_path,
            &mut receipts,
            handoff,
            &base_commit,
            &material,
        )
    } else {
        Err("trusted project source has an unsupported type".into())
    }
}

fn prepare_project_material(
    internal_url: &str,
    handoff: &ProjectHandoff,
    base_commit: &str,
    token: Option<&str>,
) -> Result<ProjectMaterial, String> {
    let temporary =
        tempfile::tempdir().map_err(|error| format!("create handoff temp dir: {error}"))?;
    let repository = temporary.path().join("internal.git");
    run_checked(
        Command::new("git")
            .arg("init")
            .arg("--bare")
            .arg(&repository),
        "initialize temporary internal repository",
    )?;
    let reference = format!("refs/heads/{}", handoff.source.default_branch);
    run_git_dir_checked(
        &repository,
        &["check-ref-format", &reference],
        "validate internal branch",
    )?;
    let refspec = format!("+{reference}:refs/heads/commitarium-canonical");
    let mut command = Command::new("git");
    command
        .arg("--git-dir")
        .arg(&repository)
        .args(["fetch", "--no-tags", "--force", "--", internal_url])
        .arg(refspec)
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
    } else if internal_url.starts_with("http://") || internal_url.starts_with("https://") {
        return Err("internal Forgejo token not found; set COMMITARIUM_FORGEJO_TOKEN_FILE".into());
    }
    run_checked(
        &mut command,
        "fetch canonical project from internal Forgejo",
    )?;
    let fetched = git_dir_line(
        &repository,
        &["rev-parse", "refs/heads/commitarium-canonical"],
    )?;
    if fetched != handoff.source.head_commit_id {
        return Err("the internal default branch advanced after the coordinator described it; retry synchronization".into());
    }
    require_commit_in_git_dir(&repository, base_commit, "project sync base")?;
    require_ancestor(
        &repository,
        base_commit,
        &handoff.source.head_commit_id,
        "the previous project watermark is not an ancestor of the canonical head",
    )?;
    let base_tree = git_dir_line(
        &repository,
        &["rev-parse", &format!("{base_commit}^{{tree}}")],
    )?;
    let head_tree = git_dir_line(
        &repository,
        &[
            "rev-parse",
            &format!("{}^{{tree}}", handoff.source.head_commit_id),
        ],
    )?;
    let patch = if base_tree == head_tree {
        None
    } else {
        let path = temporary.path().join("canonical.patch");
        let file =
            File::create(&path).map_err(|error| format!("create temporary patch: {error}"))?;
        let output = Command::new("git")
            .arg("--git-dir")
            .arg(&repository)
            .args([
                "diff",
                "--binary",
                "--full-index",
                "--no-ext-diff",
                "--no-textconv",
                "--no-renames",
            ])
            .arg(base_commit)
            .arg(&handoff.source.head_commit_id)
            .stdout(Stdio::from(file))
            .stderr(Stdio::piped())
            .output()
            .map_err(|error| format!("generate canonical project change: {error}"))?;
        require_success(output, "generate canonical project change")?;
        Some(path)
    };
    Ok(ProjectMaterial {
        _temporary: temporary,
        repository,
        patch,
        base_tree,
        head_tree,
    })
}

fn synchronize_project_repository(
    source: &Path,
    receipts_path: &Path,
    receipts: &mut ProjectReceipts,
    handoff: &ProjectHandoff,
    base_commit: &str,
    material: &ProjectMaterial,
    commit_message: &str,
) -> Result<ProjectSynchronizeResult, String> {
    if !crate::import::is_repo_root(source) {
        return Err("the recorded project source is no longer a Git repository root".into());
    }
    let (target_branch, local_base) = inspect_clean_target(source)?;
    let local_tree = git_line(source, &["rev-parse", &format!("{local_base}^{{tree}}")])?;
    if material.patch.is_none() || local_tree == material.head_tree {
        let receipt = new_project_receipt(
            handoff,
            "git",
            source,
            base_commit,
            material,
            Some(target_branch),
            Some(local_base.clone()),
            Some(local_base),
            Some(commit_message.to_string()),
            ProjectReceiptStatus::Completed,
        );
        receipts.receipts.push(receipt.clone());
        save_project_receipts(receipts_path, receipts)?;
        return Ok(project_result(&receipt, false));
    }
    let identity = read_git_identity(source)?;
    let temporary =
        tempfile::tempdir().map_err(|error| format!("create local sync temp dir: {error}"))?;
    let local_clone = temporary.path().join("local");
    prepare_local_clone(source, &local_clone, &local_base)?;
    let synthetic = synthetic_handoff(handoff, base_commit);
    import_internal_patch_objects(&local_clone, &material.repository, &synthetic)?;
    apply_patch(
        &local_clone,
        material
            .patch
            .as_ref()
            .ok_or("canonical project patch is missing")?,
    )?;
    let expected_tree = (local_tree == material.base_tree).then_some(material.head_tree.as_str());
    let local_commit = create_clean_commit(
        &local_clone,
        &local_base,
        &synthetic,
        commit_message,
        &identity,
        expected_tree,
    )?;
    import_clean_commit(source, &local_clone, &local_commit)?;
    install_clean_commit(source, &target_branch, &local_base, &local_commit)?;
    let receipt = new_project_receipt(
        handoff,
        "git",
        source,
        base_commit,
        material,
        Some(target_branch),
        Some(local_base),
        Some(local_commit),
        Some(commit_message.to_string()),
        ProjectReceiptStatus::Completed,
    );
    receipts.receipts.push(receipt.clone());
    save_project_receipts(receipts_path, receipts)?;
    Ok(project_result(&receipt, true))
}

fn synchronize_project_folder(
    source: &Path,
    receipts_path: &Path,
    receipts: &mut ProjectReceipts,
    handoff: &ProjectHandoff,
    base_commit: &str,
    material: &ProjectMaterial,
) -> Result<ProjectSynchronizeResult, String> {
    if !source.is_dir() || crate::import::is_repo_root(source) {
        return Err("the recorded project source is no longer a plain folder".into());
    }
    reject_gitlinks(&material.repository, base_commit)?;
    reject_gitlinks(&material.repository, &handoff.source.head_commit_id)?;
    if let Some(prepared_index) = receipts.receipts.iter().rposition(|receipt| {
        receipt.project_id == handoff.project_id
            && receipt.source_path == source.to_string_lossy()
            && receipt.internal_head_commit_id == handoff.source.head_commit_id
            && receipt.status == ProjectReceiptStatus::Prepared
    }) {
        let current = super::plain_folder::snapshot_folder(source, &material.head_tree)?;
        if current == material.head_tree {
            receipts.receipts[prepared_index].status = ProjectReceiptStatus::Completed;
            save_project_receipts(receipts_path, receipts)?;
            return Ok(project_result(&receipts.receipts[prepared_index], false));
        }
        if current != material.base_tree {
            return Err("a prepared project sync found partial or unrelated folder changes; inspect the folder before continuing".into());
        }
    }
    let temporary = tempfile::tempdir().map_err(|error| format!("create folder index: {error}"))?;
    let git_dir = temporary.path().join("metadata.git");
    super::plain_folder::initialize_folder_index(&git_dir, source, &material.base_tree)?;
    let current_tree = super::plain_folder::stage_folder(&git_dir, source)?;
    if material.patch.is_none() || current_tree == material.head_tree {
        let receipt = new_project_receipt(
            handoff,
            "plain_folder",
            source,
            base_commit,
            material,
            None,
            None,
            None,
            None,
            ProjectReceiptStatus::Completed,
        );
        receipts.receipts.push(receipt.clone());
        save_project_receipts(receipts_path, receipts)?;
        return Ok(project_result(&receipt, false));
    }
    if current_tree != material.base_tree {
        return Err("the plain folder differs from its previous canonical watermark; local files were not changed".into());
    }
    let patch = material
        .patch
        .as_ref()
        .ok_or("canonical project patch is missing")?;
    super::plain_folder::check_patch(&git_dir, source, patch)?;
    let receipt = new_project_receipt(
        handoff,
        "plain_folder",
        source,
        base_commit,
        material,
        None,
        None,
        None,
        None,
        ProjectReceiptStatus::Prepared,
    );
    receipts.receipts.push(receipt);
    save_project_receipts(receipts_path, receipts)?;
    let index = receipts.receipts.len() - 1;
    super::plain_folder::apply_folder_patch(&git_dir, source, patch)?;
    let result = super::plain_folder::snapshot_folder(source, &material.head_tree)?;
    if result != material.head_tree {
        return Err("the synchronized folder differs from the canonical Forgejo tree".into());
    }
    receipts.receipts[index].status = ProjectReceiptStatus::Completed;
    save_project_receipts(receipts_path, receipts)?;
    Ok(project_result(&receipts.receipts[index], true))
}

fn reject_gitlinks(repository: &Path, commit: &str) -> Result<(), String> {
    let output = Command::new("git")
        .arg("--git-dir")
        .arg(repository)
        .args(["ls-tree", "-r", commit])
        .output()
        .map_err(|error| format!("inspect canonical project entries: {error}"))?;
    if !output.status.success() {
        return Err("could not inspect canonical project entries".into());
    }
    if String::from_utf8_lossy(&output.stdout)
        .lines()
        .any(|line| line.starts_with("160000 "))
    {
        return Err("plain-folder project sync does not support nested Git repositories".into());
    }
    Ok(())
}

fn synthetic_handoff(handoff: &ProjectHandoff, base_commit: &str) -> CompletedHandoff {
    let merged_at = handoff
        .completed_features
        .last()
        .map(|item| item.merged_at.clone())
        .unwrap_or_else(|| "2000-01-01T00:00:00Z".to_string());
    CompletedHandoff {
        project_id: handoff.project_id.clone(),
        feature_id: "project_sync".to_string(),
        source: HandoffSource {
            repository: handoff.source.repository.clone(),
            base_branch: handoff.source.default_branch.clone(),
            feature_branch: handoff.source.default_branch.clone(),
            base_commit_id: base_commit.to_string(),
            approved_commit_id: handoff.source.head_commit_id.clone(),
            merge_commit_id: handoff.source.head_commit_id.clone(),
        },
        pull_request: HandoffPullRequest {
            number: 1,
            url: "internal://project-sync".to_string(),
        },
        merged_at,
    }
}

#[allow(clippy::too_many_arguments)]
fn new_project_receipt(
    handoff: &ProjectHandoff,
    source_type: &str,
    source: &Path,
    base_commit: &str,
    material: &ProjectMaterial,
    target_branch: Option<String>,
    local_base_commit_id: Option<String>,
    local_commit_id: Option<String>,
    commit_message: Option<String>,
    status: ProjectReceiptStatus,
) -> ProjectReceipt {
    ProjectReceipt {
        project_id: handoff.project_id.clone(),
        source_type: source_type.to_string(),
        source_path: source.to_string_lossy().into_owned(),
        internal_repository_owner: handoff.source.repository.owner.clone(),
        internal_repository_name: handoff.source.repository.name.clone(),
        internal_base_commit_id: base_commit.to_string(),
        internal_head_commit_id: handoff.source.head_commit_id.clone(),
        base_tree_id: material.base_tree.clone(),
        head_tree_id: material.head_tree.clone(),
        target_branch,
        local_base_commit_id,
        local_commit_id,
        commit_message,
        status,
    }
}

fn latest_completed_project_receipt<'a>(
    receipts: &'a ProjectReceipts,
    handoff: &ProjectHandoff,
    source: &ProjectSource,
) -> Option<&'a ProjectReceipt> {
    let source_path = std::fs::canonicalize(&source.path)
        .map(|path| path.to_string_lossy().into_owned())
        .unwrap_or_else(|_| source.path.clone());
    receipts.receipts.iter().rev().find(|receipt| {
        receipt.project_id == handoff.project_id
            && receipt.source_path == source_path
            && receipt.source_type == source.source_type
            && receipt.status == ProjectReceiptStatus::Completed
    })
}

fn verify_completed_project_receipt(
    receipt: &ProjectReceipt,
    source: &Path,
    handoff: &ProjectHandoff,
) -> Result<(), String> {
    if receipt.internal_repository_owner != handoff.source.repository.owner
        || receipt.internal_repository_name != handoff.source.repository.name
        || receipt.source_path != source.to_string_lossy()
        || receipt.internal_head_commit_id != handoff.source.head_commit_id
    {
        return Err(
            "the existing project sync receipt disagrees with the canonical project".into(),
        );
    }
    if receipt.source_type == "git" {
        let branch = receipt
            .target_branch
            .as_deref()
            .ok_or("project sync receipt has no branch")?;
        require_clean_target(source, branch)?;
        let local_commit = receipt
            .local_commit_id
            .as_deref()
            .ok_or("project sync receipt has no local commit")?;
        require_commit(source, local_commit, "recorded project sync commit")?;
        let head = git_line(source, &["rev-parse", "HEAD"])?;
        let status = Command::new("git")
            .arg("-C")
            .arg(source)
            .args(["merge-base", "--is-ancestor", local_commit, &head])
            .status()
            .map_err(|error| format!("verify project sync ancestry: {error}"))?;
        if !status.success() {
            return Err("the current branch no longer contains the recorded project sync".into());
        }
    } else {
        let tree = super::plain_folder::snapshot_folder(source, &receipt.head_tree_id)?;
        if tree != receipt.head_tree_id {
            return Err("the plain folder changed after its latest project synchronization".into());
        }
    }
    Ok(())
}

fn project_result(receipt: &ProjectReceipt, created: bool) -> ProjectSynchronizeResult {
    ProjectSynchronizeResult {
        project_id: receipt.project_id.clone(),
        source_type: receipt.source_type.clone(),
        source_path: receipt.source_path.clone(),
        canonical_commit_id: receipt.internal_head_commit_id.clone(),
        target_branch: receipt.target_branch.clone(),
        local_commit_id: receipt.local_commit_id.clone(),
        result_tree_id: receipt.head_tree_id.clone(),
        created,
    }
}

fn preview_project_upstream(
    source: &ProjectSource,
    receipts_path: &Path,
    handoff: &ProjectHandoff,
    selected_remote: Option<&str>,
    branch_name: &str,
) -> Result<ProjectUpstreamResult, String> {
    let (repository, receipt) = project_publication_source(source, receipts_path, handoff)?;
    super::upstream::validate_protected_branch(&repository, branch_name)?;
    let remotes = super::upstream::configured_remotes(&repository)?;
    let remote_name = choose_project_remote(
        &repository,
        receipt.target_branch.as_deref().unwrap_or_default(),
        &remotes,
        selected_remote,
    )?;
    let Some(remote_name) = remote_name else {
        return Ok(project_upstream_result(
            handoff,
            &receipt,
            &remotes,
            None,
            branch_name,
            super::upstream::UpstreamBranchStatus::SelectionRequired,
            false,
            "Choose one of the repository's configured remotes.",
        ));
    };
    let remote = remotes
        .iter()
        .find(|remote| remote.name == remote_name)
        .unwrap();
    let (status, detail) = project_probe_result(
        super::upstream::probe_remote_branch(&repository, &remote.name, branch_name),
        receipt.local_commit_id.as_deref().unwrap_or_default(),
    );
    Ok(project_upstream_result(
        handoff,
        &receipt,
        &remotes,
        Some(&remote.name),
        branch_name,
        status,
        false,
        detail,
    ))
}

fn publish_project_upstream(
    source: &ProjectSource,
    receipts_path: &Path,
    upstream_path: &Path,
    handoff: &ProjectHandoff,
    remote_name: &str,
    branch_name: &str,
) -> Result<ProjectUpstreamResult, String> {
    let (repository, receipt) = project_publication_source(source, receipts_path, handoff)?;
    super::upstream::validate_protected_branch(&repository, branch_name)?;
    let remotes = super::upstream::configured_remotes(&repository)?;
    let remote = remotes
        .iter()
        .find(|remote| remote.name == remote_name)
        .ok_or("the selected Git remote is not configured")?;
    let local_commit = receipt
        .local_commit_id
        .as_deref()
        .ok_or("project sync has no local commit")?;
    let mut publications = load_project_upstream_receipts(upstream_path)?;
    if let Some(existing) = publications.receipts.iter().find(|existing| {
        existing.project_id == handoff.project_id
            && existing.internal_head_commit_id == handoff.source.head_commit_id
            && existing.remote_name == remote.name
            && existing.branch_name == branch_name
    }) {
        if existing.repository_path != repository.to_string_lossy()
            || existing.local_commit_id != local_commit
            || existing.remote_fingerprint != remote.fingerprint
        {
            return Err("the recorded project publication disagrees with this destination".into());
        }
    }
    match super::upstream::probe_remote_branch(&repository, remote_name, branch_name) {
        super::upstream::RemoteProbe::Commit(commit) if commit == local_commit => {
            record_project_publication(
                upstream_path,
                &mut publications,
                handoff,
                &receipt,
                &repository,
                remote,
                branch_name,
            )?;
            return Ok(project_upstream_result(
                handoff,
                &receipt,
                &remotes,
                Some(remote_name),
                branch_name,
                super::upstream::UpstreamBranchStatus::AlreadyPublished,
                false,
                "The remote branch already points to the exact synchronized commit.",
            ));
        }
        super::upstream::RemoteProbe::Commit(_) => {
            return Ok(project_upstream_result(
                handoff,
                &receipt,
                &remotes,
                Some(remote_name),
                branch_name,
                super::upstream::UpstreamBranchStatus::BranchConflict,
                false,
                "That remote branch already exists with different content.",
            ));
        }
        super::upstream::RemoteProbe::Failed(status) => {
            return Ok(project_upstream_result(
                handoff,
                &receipt,
                &remotes,
                Some(remote_name),
                branch_name,
                status,
                false,
                super::upstream::remote_failure_detail(status),
            ));
        }
        super::upstream::RemoteProbe::Missing => {}
    }
    let full_ref = format!("refs/heads/{branch_name}");
    let refspec = format!("{local_commit}:{full_ref}");
    let lease = format!("--force-with-lease={full_ref}:");
    let output = Command::new("git")
        .arg("-C")
        .arg(&repository)
        .args(["push", "--porcelain", &lease, "--", remote_name, &refspec])
        .env("GIT_TERMINAL_PROMPT", "0")
        .stdin(Stdio::null())
        .output()
        .map_err(|error| format!("start system Git push: {error}"))?;
    if !output.status.success() {
        let probe = super::upstream::probe_remote_branch(&repository, remote_name, branch_name);
        if matches!(&probe, super::upstream::RemoteProbe::Commit(commit) if commit == local_commit)
        {
            record_project_publication(
                upstream_path,
                &mut publications,
                handoff,
                &receipt,
                &repository,
                remote,
                branch_name,
            )?;
        }
        let (status, detail) = project_probe_result(probe, local_commit);
        return Ok(project_upstream_result(
            handoff,
            &receipt,
            &remotes,
            Some(remote_name),
            branch_name,
            status,
            false,
            detail,
        ));
    }
    match super::upstream::probe_remote_branch(&repository, remote_name, branch_name) {
        super::upstream::RemoteProbe::Commit(commit) if commit == local_commit => {}
        probe => {
            let (status, detail) = project_probe_result(probe, local_commit);
            return Ok(project_upstream_result(
                handoff,
                &receipt,
                &remotes,
                Some(remote_name),
                branch_name,
                status,
                false,
                detail,
            ));
        }
    }
    record_project_publication(
        upstream_path,
        &mut publications,
        handoff,
        &receipt,
        &repository,
        remote,
        branch_name,
    )?;
    Ok(project_upstream_result(
        handoff,
        &receipt,
        &remotes,
        Some(remote_name),
        branch_name,
        super::upstream::UpstreamBranchStatus::Published,
        true,
        "The exact project synchronization commit was published as a new remote branch.",
    ))
}

fn project_publication_source(
    source: &ProjectSource,
    receipts_path: &Path,
    handoff: &ProjectHandoff,
) -> Result<(PathBuf, ProjectReceipt), String> {
    if source.source_type != "git" {
        return Err(
            "upstream publication requires a project imported from a Git repository".into(),
        );
    }
    let repository = std::fs::canonicalize(&source.path)
        .map_err(|error| format!("resolve imported source repository: {error}"))?;
    if !crate::import::is_repo_root(&repository) {
        return Err("the recorded project source is no longer a Git repository root".into());
    }
    let receipts = load_project_receipts(receipts_path)?;
    let receipt = latest_completed_project_receipt(&receipts, handoff, source)
        .filter(|receipt| receipt.internal_head_commit_id == handoff.source.head_commit_id)
        .ok_or("synchronize the current project locally before publishing it upstream")?
        .clone();
    verify_completed_project_receipt(&receipt, &repository, handoff)?;
    Ok((repository, receipt))
}

fn choose_project_remote(
    repository: &Path,
    target_branch: &str,
    remotes: &[super::upstream::RemoteIdentity],
    selected: Option<&str>,
) -> Result<Option<String>, String> {
    if let Some(selected) = selected {
        super::upstream::validate_remote_name(selected)?;
        return remotes
            .iter()
            .any(|remote| remote.name == selected)
            .then(|| selected.to_string())
            .ok_or_else(|| "the selected Git remote is not configured".to_string())
            .map(Some);
    }
    if !target_branch.is_empty() {
        let key = format!("branch.{target_branch}.remote");
        if let Ok(configured) = git_line(repository, &["config", "--get", &key]) {
            if configured != "." && remotes.iter().any(|remote| remote.name == configured) {
                return Ok(Some(configured));
            }
        }
    }
    if remotes.iter().any(|remote| remote.name == "origin") {
        return Ok(Some("origin".into()));
    }
    Ok((remotes.len() == 1).then(|| remotes[0].name.clone()))
}

fn project_probe_result(
    probe: super::upstream::RemoteProbe,
    local_commit: &str,
) -> (super::upstream::UpstreamBranchStatus, &'static str) {
    match probe {
        super::upstream::RemoteProbe::Missing => (
            super::upstream::UpstreamBranchStatus::Ready,
            "The protected branch name is available.",
        ),
        super::upstream::RemoteProbe::Commit(commit) if commit == local_commit => (
            super::upstream::UpstreamBranchStatus::AlreadyPublished,
            "The remote branch already points to the exact synchronized commit.",
        ),
        super::upstream::RemoteProbe::Commit(_) => (
            super::upstream::UpstreamBranchStatus::BranchConflict,
            "That remote branch already exists with different content.",
        ),
        super::upstream::RemoteProbe::Failed(status) => {
            (status, super::upstream::remote_failure_detail(status))
        }
    }
}

#[allow(clippy::too_many_arguments)]
fn project_upstream_result(
    handoff: &ProjectHandoff,
    receipt: &ProjectReceipt,
    remotes: &[super::upstream::RemoteIdentity],
    selected_remote: Option<&str>,
    branch_name: &str,
    status: super::upstream::UpstreamBranchStatus,
    created: bool,
    detail: &str,
) -> ProjectUpstreamResult {
    ProjectUpstreamResult {
        project_id: handoff.project_id.clone(),
        repository_path: receipt.source_path.clone(),
        canonical_commit_id: handoff.source.head_commit_id.clone(),
        local_commit_id: receipt.local_commit_id.clone().unwrap_or_default(),
        remotes: remotes
            .iter()
            .map(|remote| super::upstream::UpstreamRemote {
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

fn record_project_publication(
    path: &Path,
    receipts: &mut ProjectUpstreamReceipts,
    handoff: &ProjectHandoff,
    local: &ProjectReceipt,
    repository: &Path,
    remote: &super::upstream::RemoteIdentity,
    branch_name: &str,
) -> Result<(), String> {
    let receipt = ProjectUpstreamReceipt {
        project_id: handoff.project_id.clone(),
        internal_head_commit_id: handoff.source.head_commit_id.clone(),
        repository_path: repository.to_string_lossy().into_owned(),
        local_commit_id: local.local_commit_id.clone().unwrap_or_default(),
        remote_name: remote.name.clone(),
        remote_fingerprint: remote.fingerprint.clone(),
        branch_name: branch_name.to_string(),
    };
    if !receipts
        .receipts
        .iter()
        .any(|existing| existing == &receipt)
    {
        receipts.receipts.push(receipt);
        save_json(path, receipts, "project upstream receipts")?;
    }
    Ok(())
}

fn load_project_receipts(path: &Path) -> Result<ProjectReceipts, String> {
    let receipts: ProjectReceipts = load_versioned(path, "project sync receipts")?;
    for receipt in &receipts.receipts {
        validate_project_receipt(receipt)?;
    }
    Ok(receipts)
}

fn load_project_upstream_receipts(path: &Path) -> Result<ProjectUpstreamReceipts, String> {
    let receipts: ProjectUpstreamReceipts = load_versioned(path, "project upstream receipts")?;
    for receipt in &receipts.receipts {
        validate_project_upstream_receipt(receipt)?;
    }
    Ok(receipts)
}

fn validate_project_receipt(receipt: &ProjectReceipt) -> Result<(), String> {
    validate_identifier("project sync receipt project ID", &receipt.project_id)?;
    if receipt.source_type != "git" && receipt.source_type != "plain_folder" {
        return Err("project sync receipt has an unsupported source type".into());
    }
    validate_text(
        "project sync receipt source path",
        &receipt.source_path,
        32 * 1024,
    )?;
    validate_coordinate(
        "project sync receipt repository owner",
        &receipt.internal_repository_owner,
    )?;
    validate_coordinate(
        "project sync receipt repository name",
        &receipt.internal_repository_name,
    )?;
    for value in [
        &receipt.internal_base_commit_id,
        &receipt.internal_head_commit_id,
        &receipt.base_tree_id,
        &receipt.head_tree_id,
    ] {
        if !valid_object_id(value) {
            return Err("project sync receipt has an invalid internal object ID".into());
        }
    }
    if receipt.source_type == "git" {
        let branch = receipt
            .target_branch
            .as_deref()
            .ok_or("Git project sync receipt has no target branch")?;
        validate_text("project sync receipt target branch", branch, 256)?;
        if receipt.local_base_commit_id.is_none()
            && receipt.internal_base_commit_id != receipt.internal_head_commit_id
        {
            return Err("Git project sync receipt has no local base commit".into());
        }
        if receipt
            .local_base_commit_id
            .as_deref()
            .is_some_and(|value| !valid_object_id(value))
            || !receipt
                .local_commit_id
                .as_deref()
                .is_some_and(valid_object_id)
        {
            return Err("Git project sync receipt has an invalid local commit ID".into());
        }
        validate_text(
            "project sync receipt commit message",
            receipt.commit_message.as_deref().unwrap_or_default(),
            4096,
        )?;
    } else if receipt.target_branch.is_some()
        || receipt.local_base_commit_id.is_some()
        || receipt.local_commit_id.is_some()
        || receipt.commit_message.is_some()
    {
        return Err("plain-folder project sync receipt contains Git-only fields".into());
    }
    Ok(())
}

fn validate_project_upstream_receipt(receipt: &ProjectUpstreamReceipt) -> Result<(), String> {
    validate_identifier(
        "project publication receipt project ID",
        &receipt.project_id,
    )?;
    validate_text(
        "project publication receipt repository path",
        &receipt.repository_path,
        32 * 1024,
    )?;
    for value in [
        &receipt.internal_head_commit_id,
        &receipt.local_commit_id,
        &receipt.remote_fingerprint,
    ] {
        if !valid_object_id(value) {
            return Err("project publication receipt has an invalid object identity".into());
        }
    }
    super::upstream::validate_remote_name(&receipt.remote_name)?;
    validate_text(
        "project publication receipt branch",
        &receipt.branch_name,
        256,
    )?;
    if !receipt.branch_name.starts_with("commitarium/") {
        return Err("project publication receipt has an unprotected branch".into());
    }
    Ok(())
}

trait VersionedReceipts: for<'de> Deserialize<'de> + Default {
    fn version(&self) -> u32;
}

impl VersionedReceipts for ProjectReceipts {
    fn version(&self) -> u32 {
        self.version
    }
}

impl VersionedReceipts for ProjectUpstreamReceipts {
    fn version(&self) -> u32 {
        self.version
    }
}

impl VersionedReceipts for ProjectSetupReceipts {
    fn version(&self) -> u32 {
        self.version
    }
}

fn load_versioned<T: VersionedReceipts>(path: &Path, name: &str) -> Result<T, String> {
    match std::fs::read_to_string(path) {
        Ok(contents) => {
            let receipts: T = serde_json::from_str(&contents)
                .map_err(|error| format!("parse {name}: {error}"))?;
            if receipts.version() != 1 {
                return Err(format!("{name} version is unsupported"));
            }
            Ok(receipts)
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(T::default()),
        Err(error) => Err(format!("read {name}: {error}")),
    }
}

fn save_project_receipts(path: &Path, receipts: &ProjectReceipts) -> Result<(), String> {
    save_json(path, receipts, "project sync receipts")
}

fn load_project_setup_receipts(path: &Path) -> Result<ProjectSetupReceipts, String> {
    let receipts: ProjectSetupReceipts = load_versioned(path, "project workspace setup receipts")?;
    for receipt in &receipts.receipts {
        validate_identifier("workspace setup project ID", &receipt.project_id)?;
        validate_text(
            "workspace setup Idempotency-Key",
            &receipt.idempotency_key,
            512,
        )?;
        validate_text(
            "workspace setup destination path",
            &receipt.destination_path,
            32 * 1024,
        )?;
        validate_text(
            "workspace setup staging path",
            &receipt.staging_path,
            32 * 1024,
        )?;
        validate_text("workspace setup branch", &receipt.default_branch, 256)?;
        validate_text(
            "workspace setup commit message",
            &receipt.commit_message,
            4096,
        )?;
        validate_identity("workspace setup author name", &receipt.author_name)?;
        validate_identity("workspace setup author email", &receipt.author_email)?;
        for object in [
            &receipt.canonical_commit_id,
            &receipt.canonical_tree_id,
            &receipt.local_commit_id,
        ] {
            if !valid_object_id(object) {
                return Err("workspace setup receipt contains an invalid Git object ID".into());
            }
        }
    }
    Ok(receipts)
}

fn save_project_setup_receipts(path: &Path, receipts: &ProjectSetupReceipts) -> Result<(), String> {
    save_json(path, receipts, "project workspace setup receipts")
}

fn save_json<T: Serialize>(path: &Path, value: &T, name: &str) -> Result<(), String> {
    let parent = path
        .parent()
        .ok_or_else(|| format!("{name} path has no parent"))?;
    std::fs::create_dir_all(parent).map_err(|error| format!("create {name} directory: {error}"))?;
    let contents =
        serde_json::to_vec_pretty(value).map_err(|error| format!("encode {name}: {error}"))?;
    let mut temporary = tempfile::NamedTempFile::new_in(parent)
        .map_err(|error| format!("create temporary {name}: {error}"))?;
    temporary
        .write_all(&contents)
        .and_then(|_| temporary.as_file().sync_all())
        .map_err(|error| format!("write {name}: {error}"))?;
    temporary
        .persist(path)
        .map_err(|error| format!("replace {name}: {}", error.error))?;
    Ok(())
}

fn project_receipts_path(app: &AppHandle) -> Result<PathBuf, String> {
    let directory = app
        .path()
        .app_data_dir()
        .map_err(|error| format!("resolve app data directory: {error}"))?;
    Ok(directory.join(PROJECT_RECEIPTS_FILE))
}

fn receipts_path_for_features(app: &AppHandle) -> Result<PathBuf, String> {
    super::receipts_path(app)
}

fn project_upstream_receipts_path(app: &AppHandle) -> Result<PathBuf, String> {
    let directory = app
        .path()
        .app_data_dir()
        .map_err(|error| format!("resolve app data directory: {error}"))?;
    Ok(directory.join(PROJECT_UPSTREAM_RECEIPTS_FILE))
}

fn project_setup_receipts_path(app: &AppHandle) -> Result<PathBuf, String> {
    let directory = app
        .path()
        .app_data_dir()
        .map_err(|error| format!("resolve app data directory: {error}"))?;
    Ok(directory.join(PROJECT_SETUP_RECEIPTS_FILE))
}

pub(crate) fn remove_project_handoff_state(
    app: &AppHandle,
    project_id: &str,
) -> Result<(), String> {
    validate_identifier("project ID", project_id)?;
    let _guard = HANDOFF_LOCK
        .lock()
        .map_err(|_| "the local handoff lock is unavailable".to_string())?;

    let project_path = project_receipts_path(app)?;
    if project_path.exists() {
        let mut receipts = load_project_receipts(&project_path)?;
        let before = receipts.receipts.len();
        receipts
            .receipts
            .retain(|receipt| receipt.project_id != project_id);
        if receipts.receipts.len() != before {
            save_project_receipts(&project_path, &receipts)?;
        }
    }

    let upstream_path = project_upstream_receipts_path(app)?;
    if upstream_path.exists() {
        let mut receipts = load_project_upstream_receipts(&upstream_path)?;
        let before = receipts.receipts.len();
        receipts
            .receipts
            .retain(|receipt| receipt.project_id != project_id);
        if receipts.receipts.len() != before {
            save_json(&upstream_path, &receipts, "project upstream receipts")?;
        }
    }

    let setup_path = project_setup_receipts_path(app)?;
    if setup_path.exists() {
        let mut receipts = load_project_setup_receipts(&setup_path)?;
        let before = receipts.receipts.len();
        receipts
            .receipts
            .retain(|receipt| receipt.project_id != project_id);
        if receipts.receipts.len() != before {
            save_project_setup_receipts(&setup_path, &receipts)?;
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    struct ProjectFixture {
        _root: tempfile::TempDir,
        source: PathBuf,
        internal: PathBuf,
        internal_work: PathBuf,
        receipts: PathBuf,
        feature_receipts: PathBuf,
        folder_receipts: PathBuf,
        upstream_receipts: PathBuf,
        initial: String,
        handoff: ProjectHandoff,
    }

    fn checked(command: &mut Command) {
        let output = command.output().expect("run command");
        assert!(
            output.status.success(),
            "command failed: {}",
            String::from_utf8_lossy(&output.stderr)
        );
    }

    fn command(repository: &Path, args: &[&str]) {
        checked(Command::new("git").arg("-C").arg(repository).args(args));
    }

    fn line(repository: &Path, args: &[&str]) -> String {
        git_line(repository, args).expect("git line")
    }

    fn fixture() -> ProjectFixture {
        let root = tempfile::tempdir().expect("temp root");
        let source = root.path().join("source");
        let internal = root.path().join("internal.git");
        let internal_work = root.path().join("internal-work");
        let receipts = root.path().join("state/project-handoff-receipts.json");
        let feature_receipts = root.path().join("state/feature-handoff-receipts.json");
        let folder_receipts = root.path().join("state/folder-handoff-receipts.json");
        let upstream_receipts = root.path().join("state/project-upstream-receipts.json");
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
        command(&internal_work, &["config", "user.name", "Agent"]);
        command(
            &internal_work,
            &["config", "user.email", "agent@commitarium.test"],
        );
        let handoff = ProjectHandoff {
            project_id: "prj_test".into(),
            source: ProjectHandoffSource {
                repository: HandoffRepository {
                    owner: "owner".into(),
                    name: "repository".into(),
                },
                default_branch: "main".into(),
                head_commit_id: initial.clone(),
            },
            completed_features: Vec::new(),
        };
        ProjectFixture {
            _root: root,
            source,
            internal,
            internal_work,
            receipts,
            feature_receipts,
            folder_receipts,
            upstream_receipts,
            initial,
            handoff,
        }
    }

    fn add_canonical_change(
        fixture: &mut ProjectFixture,
        feature_id: &str,
        file: &str,
        contents: &str,
    ) {
        let base = line(&fixture.internal_work, &["rev-parse", "HEAD"]);
        std::fs::write(fixture.internal_work.join(file), contents).expect("write canonical file");
        command(&fixture.internal_work, &["add", "-A"]);
        command(
            &fixture.internal_work,
            &["commit", "-m", &format!("Complete {feature_id}")],
        );
        command(&fixture.internal_work, &["push", "origin", "main"]);
        let merged = line(&fixture.internal_work, &["rev-parse", "HEAD"]);
        fixture
            .handoff
            .completed_features
            .push(ProjectHandoffFeature {
                feature_id: feature_id.to_string(),
                title: feature_id.to_string(),
                base_commit_id: base,
                merge_commit_id: merged.clone(),
                merged_at: format!(
                    "2026-09-13T10:{:02}:00Z",
                    fixture.handoff.completed_features.len()
                ),
            });
        fixture.handoff.source.head_commit_id = merged;
    }

    fn feature(id: &str, base: &str, merged: &str) -> ProjectHandoffFeature {
        ProjectHandoffFeature {
            feature_id: id.to_string(),
            title: id.to_string(),
            base_commit_id: base.to_string(),
            merge_commit_id: merged.to_string(),
            merged_at: "2026-09-13T10:00:00Z".to_string(),
        }
    }

    #[test]
    fn unsynced_features_start_after_exact_watermark() {
        let a = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa";
        let b = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb";
        let c = "cccccccccccccccccccccccccccccccccccccccc";
        let completed = vec![feature("fea_one", a, b), feature("fea_two", b, c)];
        assert_eq!(features_after(&completed, Some(b)).len(), 1);
        assert_eq!(features_after(&completed, Some(c)).len(), 0);
        assert_eq!(features_after(&completed, Some(a)).len(), 2);
    }

    #[test]
    fn bootstrap_repository_keeps_only_one_clean_root_commit() {
        let fixture = fixture();
        let destination = fixture._root.path().join("exported");
        std::fs::create_dir(&destination).expect("destination");
        let expected_tree = git_dir_line(
            &fixture.internal,
            &["rev-parse", &format!("{}^{{tree}}", fixture.initial)],
        )
        .expect("canonical tree");
        let commit = create_bootstrap_repository(
            &destination,
            &fixture.internal,
            &fixture.handoff,
            "Initialize exported project",
            &GitIdentity {
                name: "Export User".into(),
                email: "export@example.test".into(),
            },
            &expected_tree,
        )
        .expect("bootstrap repository");

        assert_eq!(line(&destination, &["rev-list", "--count", "--all"]), "1");
        assert_eq!(line(&destination, &["rev-parse", "HEAD"]), commit);
        assert_eq!(
            line(&destination, &["rev-parse", "HEAD^{tree}"]),
            expected_tree
        );
        assert_eq!(
            std::fs::read_to_string(destination.join("README.md")).unwrap(),
            "initial\n"
        );
        assert!(!Command::new("git")
            .arg("-C")
            .arg(&destination)
            .args(["cat-file", "-e", &format!("{}^{{commit}}", fixture.initial)])
            .output()
            .expect("inspect pruned internal commit")
            .status
            .success());
    }

    #[test]
    fn workspace_destination_must_be_absent_or_empty() {
        let root = tempfile::tempdir().expect("root");
        let destination = root.path().join("project");
        assert!(require_available_destination(&destination).is_ok());
        std::fs::create_dir(&destination).expect("destination");
        assert!(require_available_destination(&destination).is_ok());
        std::fs::write(destination.join("keep.txt"), "user data").expect("user file");
        assert!(require_available_destination(&destination)
            .unwrap_err()
            .contains("not empty"));
    }

    #[test]
    fn project_handoff_rejects_unrecorded_canonical_head() {
        let handoff = ProjectHandoff {
            project_id: "prj_test".into(),
            source: ProjectHandoffSource {
                repository: HandoffRepository {
                    owner: "owner".into(),
                    name: "repo".into(),
                },
                default_branch: "main".into(),
                head_commit_id: "cccccccccccccccccccccccccccccccccccccccc".into(),
            },
            completed_features: vec![feature(
                "fea_one",
                "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
                "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
            )],
        };
        assert!(validate_project_handoff(&handoff, "prj_test")
            .unwrap_err()
            .contains("outside the recorded completed work orders"));
    }

    #[test]
    fn git_project_sync_chains_from_previous_internal_watermark() {
        let mut fixture = fixture();
        let source = ProjectSource {
            path: fixture.source.to_string_lossy().into_owned(),
            source_type: "git".into(),
            import_commit_id: Some(fixture.initial.clone()),
            created_by_commitarium: false,
        };
        add_canonical_change(&mut fixture, "fea_one", "one.txt", "one\n");
        let first = synchronize_project(
            &source,
            &fixture.receipts,
            &fixture.feature_receipts,
            &fixture.folder_receipts,
            &fixture.handoff,
            "Sync project one",
            fixture.internal.to_str().unwrap(),
            None,
        )
        .expect("first project sync");
        assert!(first.created);
        assert_eq!(
            std::fs::read_to_string(fixture.source.join("one.txt")).unwrap(),
            "one\n"
        );

        std::fs::write(fixture.source.join("local.txt"), "local\n").expect("local file");
        command(&fixture.source, &["add", "local.txt"]);
        command(&fixture.source, &["commit", "-m", "Local work"]);
        let local_parent = line(&fixture.source, &["rev-parse", "HEAD"]);
        add_canonical_change(&mut fixture, "fea_two", "two.txt", "two\n");

        let second = synchronize_project(
            &source,
            &fixture.receipts,
            &fixture.feature_receipts,
            &fixture.folder_receipts,
            &fixture.handoff,
            "Sync project two",
            fixture.internal.to_str().unwrap(),
            None,
        )
        .expect("second project sync");
        assert!(second.created);
        assert_eq!(line(&fixture.source, &["rev-parse", "HEAD^"]), local_parent);
        assert_eq!(
            std::fs::read_to_string(fixture.source.join("one.txt")).unwrap(),
            "one\n"
        );
        assert_eq!(
            std::fs::read_to_string(fixture.source.join("two.txt")).unwrap(),
            "two\n"
        );
        assert_eq!(
            std::fs::read_to_string(fixture.source.join("local.txt")).unwrap(),
            "local\n"
        );
        let receipts = load_project_receipts(&fixture.receipts).expect("receipts");
        assert_eq!(receipts.receipts.len(), 2);
        assert_eq!(
            receipts.receipts[1].internal_base_commit_id,
            receipts.receipts[0].internal_head_commit_id
        );
    }

    #[test]
    fn git_project_sync_conflict_leaves_real_repository_unchanged() {
        let mut fixture = fixture();
        let source = ProjectSource {
            path: fixture.source.to_string_lossy().into_owned(),
            source_type: "git".into(),
            import_commit_id: Some(fixture.initial.clone()),
            created_by_commitarium: false,
        };
        add_canonical_change(&mut fixture, "fea_one", "README.md", "canonical\n");
        std::fs::write(fixture.source.join("README.md"), "local\n").expect("local edit");
        command(&fixture.source, &["add", "README.md"]);
        command(&fixture.source, &["commit", "-m", "Local edit"]);
        let before = line(&fixture.source, &["rev-parse", "HEAD"]);

        let error = synchronize_project(
            &source,
            &fixture.receipts,
            &fixture.feature_receipts,
            &fixture.folder_receipts,
            &fixture.handoff,
            "Sync conflicting project",
            fixture.internal.to_str().unwrap(),
            None,
        )
        .unwrap_err();
        assert!(
            error.contains("conflicts with the current local HEAD"),
            "{error}"
        );
        assert_eq!(line(&fixture.source, &["rev-parse", "HEAD"]), before);
        assert_eq!(
            std::fs::read_to_string(fixture.source.join("README.md")).unwrap(),
            "local\n"
        );
        assert!(
            git_line_allow_empty(&fixture.source, &["status", "--porcelain"])
                .unwrap()
                .is_empty()
        );
    }

    #[test]
    fn project_upstream_publishes_exact_current_sync_commit() {
        let mut fixture = fixture();
        let source = ProjectSource {
            path: fixture.source.to_string_lossy().into_owned(),
            source_type: "git".into(),
            import_commit_id: Some(fixture.initial.clone()),
            created_by_commitarium: false,
        };
        add_canonical_change(&mut fixture, "fea_one", "one.txt", "one\n");
        let synchronized = synchronize_project(
            &source,
            &fixture.receipts,
            &fixture.feature_receipts,
            &fixture.folder_receipts,
            &fixture.handoff,
            "Sync project",
            fixture.internal.to_str().unwrap(),
            None,
        )
        .expect("project sync");
        let upstream = fixture._root.path().join("upstream.git");
        checked(Command::new("git").args(["init", "--bare"]).arg(&upstream));
        command(
            &fixture.source,
            &["remote", "add", "user", upstream.to_str().unwrap()],
        );

        let published = publish_project_upstream(
            &source,
            &fixture.receipts,
            &fixture.upstream_receipts,
            &fixture.handoff,
            "user",
            "commitarium/project-test",
        )
        .expect("publish project");
        assert_eq!(
            published.status,
            super::upstream::UpstreamBranchStatus::Published
        );
        let remote_commit = git_dir_line(
            &upstream,
            &["rev-parse", "refs/heads/commitarium/project-test"],
        )
        .expect("remote commit");
        assert_eq!(remote_commit, synchronized.local_commit_id.unwrap());
        let publications =
            load_project_upstream_receipts(&fixture.upstream_receipts).expect("publications");
        assert_eq!(
            publications.receipts[0].internal_head_commit_id,
            fixture.handoff.source.head_commit_id
        );
    }

    #[test]
    fn plain_folder_project_sync_chains_and_preserves_ignored_files() {
        let mut fixture = fixture();
        let folder = fixture._root.path().join("plain-folder");
        std::fs::create_dir(&folder).expect("plain folder");
        std::fs::write(folder.join("README.md"), "initial\n").expect("initial file");
        std::fs::write(folder.join(".gitignore"), "ignored/\n").expect("gitignore");
        std::fs::create_dir(folder.join("ignored")).expect("ignored dir");
        std::fs::write(folder.join("ignored/cache.bin"), "preserve\n").expect("ignored file");
        // The canonical import also needs the same ignore file so the synthetic
        // Git snapshot and the original plain folder have identical trees.
        std::fs::write(fixture.source.join(".gitignore"), "ignored/\n").expect("source ignore");
        command(&fixture.source, &["add", ".gitignore"]);
        command(&fixture.source, &["commit", "-m", "Add ignore file"]);
        command(
            &fixture.internal_work,
            &["pull", "--ff-only", "origin", "main"],
        );
        // Recreate the internal fixture at the actual plain-folder import base.
        command(
            &fixture.source,
            &["push", fixture.internal.to_str().unwrap(), "main"],
        );
        command(
            &fixture.internal_work,
            &["pull", "--ff-only", "origin", "main"],
        );
        fixture.initial = line(&fixture.internal_work, &["rev-parse", "HEAD"]);
        fixture.handoff.source.head_commit_id = fixture.initial.clone();
        let source = ProjectSource {
            path: folder.to_string_lossy().into_owned(),
            source_type: "plain_folder".into(),
            import_commit_id: Some(fixture.initial.clone()),
            created_by_commitarium: false,
        };

        add_canonical_change(&mut fixture, "fea_one", "one.txt", "one\n");
        assert!(
            synchronize_project(
                &source,
                &fixture.receipts,
                &fixture.feature_receipts,
                &fixture.folder_receipts,
                &fixture.handoff,
                "unused for folder",
                fixture.internal.to_str().unwrap(),
                None,
            )
            .expect("first folder sync")
            .created
        );
        add_canonical_change(&mut fixture, "fea_two", "two.txt", "two\n");
        assert!(
            synchronize_project(
                &source,
                &fixture.receipts,
                &fixture.feature_receipts,
                &fixture.folder_receipts,
                &fixture.handoff,
                "unused for folder",
                fixture.internal.to_str().unwrap(),
                None,
            )
            .expect("second folder sync")
            .created
        );
        assert_eq!(
            std::fs::read_to_string(folder.join("one.txt")).unwrap(),
            "one\n"
        );
        assert_eq!(
            std::fs::read_to_string(folder.join("two.txt")).unwrap(),
            "two\n"
        );
        assert_eq!(
            std::fs::read_to_string(folder.join("ignored/cache.bin")).unwrap(),
            "preserve\n"
        );
        assert!(!folder.join(".git").exists());
    }
}
