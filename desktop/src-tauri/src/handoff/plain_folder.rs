//! Safe handoff into a project that was imported from a plain folder.
//!
//! Git supplies the content model without turning the user's folder into a
//! repository: all metadata and the index live in temporary storage while the
//! original folder is used only as a work tree. A durable prepared receipt is
//! written before applying the reviewed patch so restart recovery can
//! distinguish an unchanged base, a completed result, and ambiguous partial
//! work.

use super::*;
use serde::{Deserialize, Serialize};
use std::io::Write;

const FOLDER_RECEIPTS_FILE: &str = "folder-handoff-receipts.json";

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
#[serde(rename_all = "snake_case")]
enum FolderReceiptStatus {
    Prepared,
    Completed,
}

#[derive(Clone, Debug, Deserialize, Serialize, PartialEq)]
struct FolderReceipt {
    project_id: String,
    feature_id: String,
    internal_repository_owner: String,
    internal_repository_name: String,
    internal_base_commit_id: String,
    internal_approved_commit_id: String,
    internal_merge_commit_id: String,
    destination_folder_path: String,
    base_tree_id: String,
    approved_tree_id: String,
    status: FolderReceiptStatus,
}

#[derive(Debug, Deserialize, Serialize)]
struct FolderReceipts {
    version: u32,
    receipts: Vec<FolderReceipt>,
}

impl Default for FolderReceipts {
    fn default() -> Self {
        Self {
            version: 1,
            receipts: Vec::new(),
        }
    }
}

#[derive(Debug, Serialize)]
pub struct FolderSynchronizeResult {
    project_id: String,
    feature_id: String,
    folder_path: String,
    result_tree_id: String,
    created: bool,
}

struct InternalMaterial {
    _temporary: tempfile::TempDir,
    patch: PathBuf,
    base_tree: String,
    approved_tree: String,
}

/// Synchronize one completed work order into its originally imported plain
/// folder. The folder remains a non-Git directory and ignored local files are
/// not included in the comparison or modified by the reviewed patch.
#[tauri::command]
pub async fn synchronize_feature_to_folder(
    app: AppHandle,
    project_id: String,
    feature_id: String,
) -> Result<FolderSynchronizeResult, String> {
    validate_identifier("project ID", &project_id)?;
    validate_identifier("feature ID", &feature_id)?;

    let handoff = fetch_handoff(&project_id, &feature_id).await?;
    if handoff.project_id != project_id || handoff.feature_id != feature_id {
        return Err("coordinator returned a handoff for another work order".into());
    }
    let source = super::super::import::get_project_source(app.clone(), project_id.clone())?
        .ok_or("this project has no trusted local source mapping")?;
    let receipts_path = folder_receipts_path(&app)?;
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
        synchronize_folder(
            Path::new(&source),
            &receipts_path,
            &handoff,
            &internal_url,
            token.as_deref(),
        )
    })
    .await
    .map_err(|e| format!("plain-folder synchronization task failed: {e}"))?
}

fn synchronize_folder(
    source_path: &Path,
    receipts_path: &Path,
    handoff: &CompletedHandoff,
    internal_url: &str,
    token: Option<&str>,
) -> Result<FolderSynchronizeResult, String> {
    validate_handoff(handoff)?;
    let source_path = std::fs::canonicalize(source_path)
        .map_err(|e| format!("resolve imported source folder: {e}"))?;
    if !source_path.is_dir() {
        return Err("the recorded project source is no longer a folder".into());
    }
    if super::super::import::is_repo_root(&source_path) {
        return Err(
            "this source is a Git repository; use synchronize_feature_locally instead".into(),
        );
    }

    let mut receipts = load_folder_receipts(receipts_path)?;
    let matches: Vec<usize> = receipts
        .receipts
        .iter()
        .enumerate()
        .filter(|(_, receipt)| {
            receipt.project_id == handoff.project_id && receipt.feature_id == handoff.feature_id
        })
        .map(|(index, _)| index)
        .collect();
    if matches.len() > 1 {
        return Err("more than one plain-folder handoff record exists for this work order".into());
    }

    if let Some(index) = matches.first().copied() {
        let receipt = receipts.receipts[index].clone();
        verify_receipt_request(&receipt, &source_path, handoff)?;
        let current_tree = snapshot_folder(&source_path, &receipt.approved_tree_id)?;
        match receipt.status {
            FolderReceiptStatus::Completed => {
                if current_tree != receipt.approved_tree_id {
                    return Err(
                        "the plain folder changed after this work order was synchronized".into(),
                    );
                }
                return Ok(result_from_receipt(&receipt, false));
            }
            FolderReceiptStatus::Prepared if current_tree == receipt.approved_tree_id => {
                receipts.receipts[index].status = FolderReceiptStatus::Completed;
                save_folder_receipts(receipts_path, &receipts)?;
                return Ok(result_from_receipt(&receipts.receipts[index], false));
            }
            FolderReceiptStatus::Prepared if current_tree != receipt.base_tree_id => {
                return Err(
                    "the prepared plain-folder handoff found partial or unrelated local changes; inspect the folder before continuing"
                        .into(),
                );
            }
            FolderReceiptStatus::Prepared => {}
        }
    }

    let material = prepare_internal_material(internal_url, handoff, token)?;
    if let Some(index) = matches.first().copied() {
        let receipt = &receipts.receipts[index];
        if receipt.base_tree_id != material.base_tree
            || receipt.approved_tree_id != material.approved_tree
        {
            return Err("the verified Forgejo trees disagree with the prepared handoff".into());
        }
    }

    let folder_index = tempfile::tempdir().map_err(|e| format!("create folder index: {e}"))?;
    let git_dir = folder_index.path().join("metadata.git");
    initialize_folder_index(&git_dir, &source_path, &material.base_tree)?;
    let current_tree = stage_folder(&git_dir, &source_path)?;

    if matches.is_empty() && current_tree == material.approved_tree {
        let receipt = new_receipt(
            &source_path,
            handoff,
            &material.base_tree,
            &material.approved_tree,
            FolderReceiptStatus::Completed,
        );
        receipts.receipts.push(receipt.clone());
        save_folder_receipts(receipts_path, &receipts)?;
        return Ok(result_from_receipt(&receipt, false));
    }
    if current_tree != material.base_tree {
        return Err(
            "the plain folder no longer matches the completed work order's internal base; local files were not changed"
                .into(),
        );
    }

    check_patch(&git_dir, &source_path, &material.patch)?;
    let index = if let Some(index) = matches.first().copied() {
        index
    } else {
        let receipt = new_receipt(
            &source_path,
            handoff,
            &material.base_tree,
            &material.approved_tree,
            FolderReceiptStatus::Prepared,
        );
        receipts.receipts.push(receipt);
        save_folder_receipts(receipts_path, &receipts)?;
        receipts.receipts.len() - 1
    };

    apply_folder_patch(&git_dir, &source_path, &material.patch)?;
    let result_tree = snapshot_folder(&source_path, &material.approved_tree)?;
    if result_tree != material.approved_tree {
        return Err(
            "the synchronized plain-folder tree differs from the approved Forgejo tree".into(),
        );
    }

    receipts.receipts[index].status = FolderReceiptStatus::Completed;
    save_folder_receipts(receipts_path, &receipts)?;
    Ok(result_from_receipt(&receipts.receipts[index], true))
}

fn prepare_internal_material(
    internal_url: &str,
    handoff: &CompletedHandoff,
    token: Option<&str>,
) -> Result<InternalMaterial, String> {
    let temporary = tempfile::tempdir().map_err(|e| format!("create handoff temp dir: {e}"))?;
    let repository = temporary.path().join("internal.git");
    let patch = temporary.path().join("approved.patch");
    prepare_internal_repository(&repository, internal_url, handoff, token)?;
    verify_internal_history(&repository, handoff)?;
    reject_gitlinks(&repository, &handoff.source.base_commit_id)?;
    reject_gitlinks(&repository, &handoff.source.approved_commit_id)?;
    let base_tree = git_dir_line(
        &repository,
        &[
            "rev-parse",
            &format!("{}^{{tree}}", handoff.source.base_commit_id),
        ],
    )?;
    let approved_tree = git_dir_line(
        &repository,
        &[
            "rev-parse",
            &format!("{}^{{tree}}", handoff.source.approved_commit_id),
        ],
    )?;
    write_approved_patch(&repository, handoff, &patch)?;
    Ok(InternalMaterial {
        _temporary: temporary,
        patch,
        base_tree,
        approved_tree,
    })
}

fn reject_gitlinks(repository: &Path, commit: &str) -> Result<(), String> {
    let output = Command::new("git")
        .arg("--git-dir")
        .arg(repository)
        .args(["ls-tree", "-r", commit])
        .output()
        .map_err(|e| format!("inspect internal project entries: {e}"))?;
    if !output.status.success() {
        return Err(format!(
            "inspect internal project entries: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    if String::from_utf8_lossy(&output.stdout)
        .lines()
        .any(|line| line.starts_with("160000 "))
    {
        return Err("plain-folder handoff does not support nested Git repositories".into());
    }
    Ok(())
}

fn initialize_folder_index(
    git_dir: &Path,
    folder: &Path,
    expected_tree: &str,
) -> Result<(), String> {
    let object_format = match expected_tree.len() {
        40 => "sha1",
        64 => "sha256",
        _ => return Err("expected folder tree has an unsupported object format".into()),
    };
    run_checked(
        plain_git_command(git_dir, folder)
            .args(["init", "--quiet"])
            .arg(format!("--object-format={object_format}")),
        "initialize temporary plain-folder index",
    )
}

fn stage_folder(git_dir: &Path, folder: &Path) -> Result<String, String> {
    run_plain_git_checked(
        git_dir,
        folder,
        &["add", "-A"],
        "inspect plain-folder contents",
    )?;
    plain_git_line(git_dir, folder, &["write-tree"])
}

fn snapshot_folder(folder: &Path, expected_tree: &str) -> Result<String, String> {
    let temporary = tempfile::tempdir().map_err(|e| format!("create folder snapshot: {e}"))?;
    let git_dir = temporary.path().join("metadata.git");
    initialize_folder_index(&git_dir, folder, expected_tree)?;
    stage_folder(&git_dir, folder)
}

fn check_patch(git_dir: &Path, folder: &Path, patch: &Path) -> Result<(), String> {
    run_checked(
        plain_git_command(git_dir, folder)
            .args(["apply", "--check", "--binary", "--whitespace=nowarn", "--"])
            .arg(patch),
        "check reviewed change against plain folder",
    )
}

fn apply_folder_patch(git_dir: &Path, folder: &Path, patch: &Path) -> Result<(), String> {
    run_checked(
        plain_git_command(git_dir, folder)
            .args(["apply", "--binary", "--whitespace=nowarn", "--"])
            .arg(patch),
        "apply reviewed change to plain folder",
    )
}

fn plain_git_command(git_dir: &Path, folder: &Path) -> Command {
    let mut command = Command::new("git");
    // `git apply` resolves paths from the process directory even when
    // GIT_WORK_TREE is set, so pin both to the selected folder. Without this,
    // a patch could be applied relative to the desktop process directory.
    command
        .current_dir(folder)
        .env("GIT_DIR", git_dir)
        .env("GIT_WORK_TREE", folder);
    command
}

fn plain_git_line(git_dir: &Path, folder: &Path, args: &[&str]) -> Result<String, String> {
    let output = plain_git_command(git_dir, folder)
        .args(args)
        .output()
        .map_err(|e| format!("run git {}: {e}", args.join(" ")))?;
    output_line(output, args)
}

fn run_plain_git_checked(
    git_dir: &Path,
    folder: &Path,
    args: &[&str],
    action: &str,
) -> Result<(), String> {
    run_checked(plain_git_command(git_dir, folder).args(args), action)
}

fn new_receipt(
    folder: &Path,
    handoff: &CompletedHandoff,
    base_tree: &str,
    approved_tree: &str,
    status: FolderReceiptStatus,
) -> FolderReceipt {
    FolderReceipt {
        project_id: handoff.project_id.clone(),
        feature_id: handoff.feature_id.clone(),
        internal_repository_owner: handoff.source.repository.owner.clone(),
        internal_repository_name: handoff.source.repository.name.clone(),
        internal_base_commit_id: handoff.source.base_commit_id.clone(),
        internal_approved_commit_id: handoff.source.approved_commit_id.clone(),
        internal_merge_commit_id: handoff.source.merge_commit_id.clone(),
        destination_folder_path: folder.to_string_lossy().into_owned(),
        base_tree_id: base_tree.to_string(),
        approved_tree_id: approved_tree.to_string(),
        status,
    }
}

fn verify_receipt_request(
    receipt: &FolderReceipt,
    folder: &Path,
    handoff: &CompletedHandoff,
) -> Result<(), String> {
    let exact = receipt.project_id == handoff.project_id
        && receipt.feature_id == handoff.feature_id
        && receipt.internal_repository_owner == handoff.source.repository.owner
        && receipt.internal_repository_name == handoff.source.repository.name
        && receipt.internal_base_commit_id == handoff.source.base_commit_id
        && receipt.internal_approved_commit_id == handoff.source.approved_commit_id
        && receipt.internal_merge_commit_id == handoff.source.merge_commit_id
        && receipt.destination_folder_path == folder.to_string_lossy();
    if !exact {
        return Err("an existing plain-folder handoff record disagrees with this request".into());
    }
    Ok(())
}

fn result_from_receipt(receipt: &FolderReceipt, created: bool) -> FolderSynchronizeResult {
    FolderSynchronizeResult {
        project_id: receipt.project_id.clone(),
        feature_id: receipt.feature_id.clone(),
        folder_path: receipt.destination_folder_path.clone(),
        result_tree_id: receipt.approved_tree_id.clone(),
        created,
    }
}

fn folder_receipts_path(app: &AppHandle) -> Result<PathBuf, String> {
    let directory = app
        .path()
        .app_data_dir()
        .map_err(|e| format!("resolve app data directory: {e}"))?;
    Ok(directory.join(FOLDER_RECEIPTS_FILE))
}

fn load_folder_receipts(path: &Path) -> Result<FolderReceipts, String> {
    match std::fs::read_to_string(path) {
        Ok(contents) => {
            let receipts: FolderReceipts = serde_json::from_str(&contents)
                .map_err(|e| format!("parse plain-folder handoff records: {e}"))?;
            if receipts.version != 1 {
                return Err("plain-folder handoff record version is unsupported".into());
            }
            for receipt in &receipts.receipts {
                validate_folder_receipt(receipt)?;
            }
            Ok(receipts)
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(FolderReceipts::default()),
        Err(error) => Err(format!("read plain-folder handoff records: {error}")),
    }
}

fn validate_folder_receipt(receipt: &FolderReceipt) -> Result<(), String> {
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
        ("receipt base tree", receipt.base_tree_id.as_str()),
        ("receipt approved tree", receipt.approved_tree_id.as_str()),
    ] {
        if !valid_object_id(value) {
            return Err(format!("{name} is not a valid Git object ID"));
        }
    }
    validate_text(
        "receipt destination folder",
        &receipt.destination_folder_path,
        32 * 1024,
    )
}

fn save_folder_receipts(path: &Path, receipts: &FolderReceipts) -> Result<(), String> {
    let parent = path
        .parent()
        .ok_or("plain-folder handoff record path has no parent")?;
    std::fs::create_dir_all(parent)
        .map_err(|e| format!("create plain-folder handoff record directory: {e}"))?;
    let contents = serde_json::to_vec_pretty(receipts)
        .map_err(|e| format!("encode plain-folder handoff records: {e}"))?;
    let mut temporary = tempfile::NamedTempFile::new_in(parent)
        .map_err(|e| format!("create temporary plain-folder handoff record: {e}"))?;
    temporary
        .write_all(&contents)
        .and_then(|_| temporary.as_file().sync_all())
        .map_err(|e| format!("write plain-folder handoff records: {e}"))?;
    temporary
        .persist(path)
        .map_err(|e| format!("replace plain-folder handoff records: {}", e.error))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

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

    struct FolderFixture {
        _root: tempfile::TempDir,
        source: PathBuf,
        internal: PathBuf,
        internal_work: PathBuf,
        receipts: PathBuf,
    }

    fn fixture() -> FolderFixture {
        let root = tempfile::tempdir().expect("temp root");
        let source = root.path().join("plain-source");
        let internal = root.path().join("internal.git");
        let internal_work = root.path().join("internal-work");
        let receipts = root.path().join("state/folder-handoff-receipts.json");
        std::fs::create_dir(&source).expect("source dir");
        std::fs::write(source.join(".gitignore"), "ignored/\n").expect("gitignore");
        std::fs::write(source.join("README.md"), "initial\n").expect("initial file");
        std::fs::write(source.join("obsolete.txt"), "remove me\n").expect("obsolete file");
        std::fs::create_dir(source.join("ignored")).expect("ignored dir");
        std::fs::write(source.join("ignored/cache.bin"), "leave me\n").expect("ignored file");

        let seed = root.path().join("seed");
        std::fs::create_dir(&seed).expect("seed dir");
        checked(
            Command::new("git")
                .arg("-C")
                .arg(&seed)
                .args(["init", "--initial-branch=main"]),
        );
        command(&seed, &["config", "user.name", "Import"]);
        command(&seed, &["config", "user.email", "import@example.test"]);
        std::fs::copy(source.join(".gitignore"), seed.join(".gitignore")).expect("copy ignore");
        std::fs::copy(source.join("README.md"), seed.join("README.md")).expect("copy readme");
        std::fs::copy(source.join("obsolete.txt"), seed.join("obsolete.txt"))
            .expect("copy obsolete");
        command(&seed, &["add", "-A"]);
        command(&seed, &["commit", "-m", "Import"]);
        checked(
            Command::new("git")
                .args(["clone", "--quiet", "--bare", "--"])
                .arg(&seed)
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
        FolderFixture {
            _root: root,
            source,
            internal,
            internal_work,
            receipts,
        }
    }

    fn add_feature(fixture: &FolderFixture, branch: &str) -> CompletedHandoff {
        command(&fixture.internal_work, &["checkout", "--quiet", "main"]);
        command(
            &fixture.internal_work,
            &["checkout", "--quiet", "-b", branch],
        );
        std::fs::write(fixture.internal_work.join("README.md"), "approved\n")
            .expect("update readme");
        std::fs::write(fixture.internal_work.join("added.txt"), "new\n").expect("add file");
        std::fs::write(
            fixture.internal_work.join("binary.dat"),
            [0_u8, 1, 2, 0, 255],
        )
        .expect("add binary file");
        std::fs::remove_file(fixture.internal_work.join("obsolete.txt"))
            .expect("remove obsolete file");
        command(&fixture.internal_work, &["add", "-A"]);
        command(&fixture.internal_work, &["commit", "-m", "Agent change"]);
        let approved = line(&fixture.internal_work, &["rev-parse", "HEAD"]);
        let base = line(&fixture.internal_work, &["rev-parse", "HEAD^"]);
        command(
            &fixture.internal_work,
            &["push", "--quiet", "origin", branch],
        );
        command(&fixture.internal_work, &["checkout", "--quiet", "main"]);
        command(
            &fixture.internal_work,
            &["merge", "--quiet", "--no-ff", branch, "-m", "Merge"],
        );
        let merged = line(&fixture.internal_work, &["rev-parse", "HEAD"]);
        command(
            &fixture.internal_work,
            &["push", "--quiet", "origin", "main"],
        );
        CompletedHandoff {
            project_id: "prj_plain".into(),
            feature_id: format!("fea_{}", branch.replace('/', "_")),
            source: HandoffSource {
                repository: HandoffRepository {
                    owner: "commitarium".into(),
                    name: "plain-project".into(),
                },
                base_branch: "main".into(),
                feature_branch: branch.into(),
                base_commit_id: base,
                approved_commit_id: approved,
                merge_commit_id: merged,
            },
            pull_request: HandoffPullRequest {
                number: 8,
                url: "http://127.0.0.1:3001/commitarium/plain-project/pulls/8".into(),
            },
            merged_at: "2026-09-11T16:00:00Z".into(),
        }
    }

    #[test]
    fn synchronizes_plain_folder_and_preserves_ignored_files() {
        let fixture = fixture();
        let handoff = add_feature(&fixture, "commitarium/plain-one");
        let result = synchronize_folder(
            &fixture.source,
            &fixture.receipts,
            &handoff,
            &fixture.internal.to_string_lossy(),
            None,
        )
        .expect("sync folder");
        assert!(result.created);
        assert_eq!(
            std::fs::read_to_string(fixture.source.join("README.md")).expect("read result"),
            "approved\n"
        );
        assert_eq!(
            std::fs::read_to_string(fixture.source.join("added.txt")).expect("read added"),
            "new\n"
        );
        assert_eq!(
            std::fs::read(fixture.source.join("binary.dat")).expect("read binary"),
            [0_u8, 1, 2, 0, 255]
        );
        assert!(!fixture.source.join("obsolete.txt").exists());
        assert_eq!(
            std::fs::read_to_string(fixture.source.join("ignored/cache.bin"))
                .expect("read ignored"),
            "leave me\n"
        );
        assert!(!fixture.source.join(".git").exists());

        let retried = synchronize_folder(
            &fixture.source,
            &fixture.receipts,
            &handoff,
            &fixture.internal.to_string_lossy(),
            None,
        )
        .expect("retry folder sync");
        assert!(!retried.created);
        assert_eq!(retried.result_tree_id, result.result_tree_id);
    }

    #[test]
    fn adopts_completed_folder_after_prepared_receipt() {
        let fixture = fixture();
        let handoff = add_feature(&fixture, "commitarium/plain-recovery");
        let material =
            prepare_internal_material(&fixture.internal.to_string_lossy(), &handoff, None)
                .expect("material");
        let folder_index = tempfile::tempdir().expect("folder index");
        let git_dir = folder_index.path().join("metadata.git");
        initialize_folder_index(&git_dir, &fixture.source, &material.base_tree)
            .expect("init index");
        assert_eq!(
            stage_folder(&git_dir, &fixture.source).expect("snapshot"),
            material.base_tree
        );
        check_patch(&git_dir, &fixture.source, &material.patch).expect("check patch");
        let receipt = new_receipt(
            &std::fs::canonicalize(&fixture.source).expect("canonical source"),
            &handoff,
            &material.base_tree,
            &material.approved_tree,
            FolderReceiptStatus::Prepared,
        );
        save_folder_receipts(
            &fixture.receipts,
            &FolderReceipts {
                version: 1,
                receipts: vec![receipt],
            },
        )
        .expect("save prepared");
        apply_folder_patch(&git_dir, &fixture.source, &material.patch).expect("apply patch");

        let adopted = synchronize_folder(
            &fixture.source,
            &fixture.receipts,
            &handoff,
            &fixture.internal.to_string_lossy(),
            None,
        )
        .expect("adopt completed folder");
        assert!(!adopted.created);
        let receipts = load_folder_receipts(&fixture.receipts).expect("receipts");
        assert_eq!(receipts.receipts[0].status, FolderReceiptStatus::Completed);
    }

    #[test]
    fn retries_unchanged_folder_after_prepared_receipt() {
        let fixture = fixture();
        let handoff = add_feature(&fixture, "commitarium/plain-prepared");
        let material =
            prepare_internal_material(&fixture.internal.to_string_lossy(), &handoff, None)
                .expect("material");
        let receipt = new_receipt(
            &std::fs::canonicalize(&fixture.source).expect("canonical source"),
            &handoff,
            &material.base_tree,
            &material.approved_tree,
            FolderReceiptStatus::Prepared,
        );
        save_folder_receipts(
            &fixture.receipts,
            &FolderReceipts {
                version: 1,
                receipts: vec![receipt],
            },
        )
        .expect("save prepared");

        let retried = synchronize_folder(
            &fixture.source,
            &fixture.receipts,
            &handoff,
            &fixture.internal.to_string_lossy(),
            None,
        )
        .expect("retry prepared handoff");
        assert!(retried.created);
        assert_eq!(
            std::fs::read_to_string(fixture.source.join("README.md")).expect("read result"),
            "approved\n"
        );
        assert_eq!(
            load_folder_receipts(&fixture.receipts)
                .expect("receipts")
                .receipts[0]
                .status,
            FolderReceiptStatus::Completed
        );
    }

    #[test]
    fn refuses_changed_or_partially_updated_plain_folder() {
        let fixture = fixture();
        let handoff = add_feature(&fixture, "commitarium/plain-conflict");
        std::fs::write(fixture.source.join("README.md"), "user edit\n").expect("user edit");
        let error = synchronize_folder(
            &fixture.source,
            &fixture.receipts,
            &handoff,
            &fixture.internal.to_string_lossy(),
            None,
        )
        .expect_err("changed folder must fail");
        assert!(error.contains("no longer matches"), "{error}");
        assert_eq!(
            std::fs::read_to_string(fixture.source.join("README.md")).expect("read user edit"),
            "user edit\n"
        );

        let material =
            prepare_internal_material(&fixture.internal.to_string_lossy(), &handoff, None)
                .expect("material");
        let receipt = new_receipt(
            &std::fs::canonicalize(&fixture.source).expect("canonical source"),
            &handoff,
            &material.base_tree,
            &material.approved_tree,
            FolderReceiptStatus::Prepared,
        );
        save_folder_receipts(
            &fixture.receipts,
            &FolderReceipts {
                version: 1,
                receipts: vec![receipt],
            },
        )
        .expect("save prepared");
        let error = synchronize_folder(
            &fixture.source,
            &fixture.receipts,
            &handoff,
            &fixture.internal.to_string_lossy(),
            None,
        )
        .expect_err("partial state must fail");
        assert!(error.contains("partial or unrelated"), "{error}");
    }
}
