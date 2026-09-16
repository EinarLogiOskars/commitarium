//! Trusted-host Git provider discovery and new-repository publication.
//!
//! The renderer selects only a fixed provider and declarative destination.
//! Provider credentials remain in each provider CLI and never cross IPC.

use serde::{Deserialize, Serialize};
use std::ffi::OsString;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};
use std::sync::Mutex;
use tauri::{AppHandle, Manager};

const REMOTE_RECEIPTS_FILE: &str = "project-remote-setup-receipts.json";
static REMOTE_SETUP_LOCK: Mutex<()> = Mutex::new(());

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct GitProviderProbe {
    provider: String,
    display_name: String,
    installed: bool,
    authenticated: bool,
    can_create: bool,
    account: Option<String>,
    host: Option<String>,
    detail: Option<String>,
    install_url: String,
}

#[derive(Clone, Debug, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct CreateProjectRemoteRequest {
    project_id: String,
    provider: String,
    namespace: String,
    repository_name: String,
    visibility: String,
    azure_project: Option<String>,
    idempotency_key: String,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ProjectRemoteResult {
    project_id: String,
    provider: String,
    repository_path: String,
    remote_name: String,
    remote_url: String,
    web_url: String,
    default_branch: String,
    local_commit_id: String,
    created: bool,
    pushed: bool,
}

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
enum RemoteSetupStatus {
    Prepared,
    Completed,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
struct RemoteSetupReceipt {
    idempotency_key: String,
    project_id: String,
    provider: String,
    namespace: String,
    repository_name: String,
    visibility: String,
    azure_project: Option<String>,
    repository_path: String,
    default_branch: String,
    canonical_commit_id: String,
    local_commit_id: String,
    remote_name: String,
    remote_url: Option<String>,
    web_url: Option<String>,
    status: RemoteSetupStatus,
}

#[derive(Debug, Deserialize, Serialize)]
struct RemoteSetupReceipts {
    version: u32,
    receipts: Vec<RemoteSetupReceipt>,
}

impl Default for RemoteSetupReceipts {
    fn default() -> Self {
        Self {
            version: 1,
            receipts: Vec::new(),
        }
    }
}

#[derive(Clone, Debug)]
struct ProviderRepository {
    remote_url: String,
    web_url: String,
}

#[derive(Clone, Debug)]
pub(crate) struct CreatedRemoteWatermark {
    pub(crate) remote_name: String,
    pub(crate) default_branch: String,
    pub(crate) canonical_commit_id: String,
}

#[tauri::command]
pub async fn probe_git_providers() -> Result<Vec<GitProviderProbe>, String> {
    tokio::task::spawn_blocking(|| vec![probe_github(), probe_gitlab(), probe_azure_devops()])
        .await
        .map_err(|error| format!("Git provider probe task failed: {error}"))
}

#[tauri::command]
pub async fn create_project_remote(
    app: AppHandle,
    request: CreateProjectRemoteRequest,
) -> Result<ProjectRemoteResult, String> {
    validate_request(&request)?;
    let receipts_path = remote_receipts_path(&app)?;
    let replay_path = receipts_path.clone();
    let replay_request = request.clone();
    if let Some(result) =
        tokio::task::spawn_blocking(move || replay_completed_remote(&replay_path, &replay_request))
            .await
            .map_err(|error| format!("project remote replay task failed: {error}"))??
    {
        return Ok(result);
    }
    let pending_path = receipts_path.clone();
    let pending_request = request.clone();
    let pending_context = tokio::task::spawn_blocking(move || {
        prepared_remote_context(&pending_path, &pending_request)
    })
    .await
    .map_err(|error| format!("project remote recovery task failed: {error}"))??;
    let context = match pending_context {
        Some(context) => context,
        None => crate::handoff::project::new_remote_context(&app, &request.project_id).await?,
    };
    tokio::task::spawn_blocking(move || {
        let _guard = REMOTE_SETUP_LOCK
            .lock()
            .map_err(|_| "the project remote setup lock is unavailable".to_string())?;
        create_or_resume_remote(&receipts_path, &request, context)
    })
    .await
    .map_err(|error| format!("project remote setup task failed: {error}"))?
}

fn replay_completed_remote(
    receipts_path: &Path,
    request: &CreateProjectRemoteRequest,
) -> Result<Option<ProjectRemoteResult>, String> {
    let receipts = load_receipts(receipts_path)?;
    let Some(receipt) = receipts
        .receipts
        .iter()
        .find(|receipt| receipt.idempotency_key == request.idempotency_key)
    else {
        return Ok(None);
    };
    if receipt.project_id != request.project_id
        || receipt.provider != request.provider
        || receipt.namespace != request.namespace
        || receipt.repository_name != request.repository_name
        || receipt.visibility != request.visibility
        || receipt.azure_project != request.azure_project
    {
        return Err(
            "this Idempotency-Key was already used for different remote setup parameters".into(),
        );
    }
    if receipt.status != RemoteSetupStatus::Completed {
        return Ok(None);
    }
    let repository = std::fs::canonicalize(&receipt.repository_path)
        .map_err(|error| format!("resolve recorded local project repository: {error}"))?;
    verify_remote_branch(&repository, receipt)?;
    Ok(Some(remote_result(receipt, false, true)))
}

fn prepared_remote_context(
    receipts_path: &Path,
    request: &CreateProjectRemoteRequest,
) -> Result<Option<crate::handoff::project::NewRemoteContext>, String> {
    let receipts = load_receipts(receipts_path)?;
    let Some(receipt) = receipts
        .receipts
        .iter()
        .find(|receipt| receipt.idempotency_key == request.idempotency_key)
    else {
        return Ok(None);
    };
    if receipt.status != RemoteSetupStatus::Prepared {
        return Ok(None);
    }
    if receipt.project_id != request.project_id
        || receipt.provider != request.provider
        || receipt.namespace != request.namespace
        || receipt.repository_name != request.repository_name
        || receipt.visibility != request.visibility
        || receipt.azure_project != request.azure_project
    {
        return Err(
            "this Idempotency-Key was already used for different remote setup parameters".into(),
        );
    }
    let repository_path = std::fs::canonicalize(&receipt.repository_path)
        .map_err(|error| format!("resolve prepared local project repository: {error}"))?;
    let head = git_output_line(&repository_path, &["rev-parse", "HEAD"])?;
    let branch = git_output_line(
        &repository_path,
        &["symbolic-ref", "--quiet", "--short", "HEAD"],
    )?;
    let status = git_output_line_allow_empty(
        &repository_path,
        &["status", "--porcelain=v1", "--untracked-files=all"],
    )?;
    if head != receipt.local_commit_id || branch != receipt.default_branch || !status.is_empty() {
        return Err("the local repository changed after remote setup was prepared".into());
    }
    Ok(Some(crate::handoff::project::NewRemoteContext {
        repository_path,
        default_branch: receipt.default_branch.clone(),
        canonical_commit_id: receipt.canonical_commit_id.clone(),
        local_commit_id: receipt.local_commit_id.clone(),
    }))
}

fn probe_github() -> GitProviderProbe {
    let Some(cli) = find_cli("gh") else {
        return unavailable_probe(
            "github",
            "GitHub",
            "https://cli.github.com/",
            "GitHub CLI is not installed.",
        );
    };
    let status = run(
        &cli,
        &["auth", "status", "--hostname", "github.com", "--active"],
    );
    let authenticated = status.as_ref().is_ok_and(|output| output.status.success());
    let account = authenticated
        .then(|| {
            run_line(
                &cli,
                &["api", "--hostname", "github.com", "user", "--jq", ".login"],
            )
        })
        .flatten();
    GitProviderProbe {
        provider: "github".into(),
        display_name: "GitHub".into(),
        installed: true,
        authenticated,
        can_create: authenticated,
        account,
        host: authenticated.then(|| "github.com".into()),
        detail: (!authenticated).then(|| "GitHub CLI is installed but not signed in.".into()),
        install_url: "https://cli.github.com/".into(),
    }
}

fn probe_gitlab() -> GitProviderProbe {
    let Some(cli) = find_cli("glab") else {
        return unavailable_probe(
            "gitlab",
            "GitLab",
            "https://docs.gitlab.com/cli/",
            "GitLab CLI is not installed.",
        );
    };
    let status = run(&cli, &["auth", "status"]);
    let authenticated = status.as_ref().is_ok_and(|output| output.status.success());
    let account = authenticated
        .then(|| run_line(&cli, &["api", "user", "--jq", ".username"]))
        .flatten();
    GitProviderProbe {
        provider: "gitlab".into(),
        display_name: "GitLab".into(),
        installed: true,
        authenticated,
        can_create: authenticated,
        account,
        host: authenticated.then(|| "gitlab.com".into()),
        detail: (!authenticated).then(|| "GitLab CLI is installed but not signed in.".into()),
        install_url: "https://docs.gitlab.com/cli/".into(),
    }
}

fn probe_azure_devops() -> GitProviderProbe {
    let Some(cli) = find_cli("az") else {
        return unavailable_probe(
            "azure_devops",
            "Azure DevOps",
            "https://learn.microsoft.com/cli/azure/install-azure-cli",
            "Azure CLI is not installed.",
        );
    };
    let account = run_json(&cli, &["account", "show", "--output", "json"]);
    let extension = run(
        &cli,
        &[
            "extension",
            "show",
            "--name",
            "azure-devops",
            "--output",
            "none",
        ],
    )
    .is_ok_and(|output| output.status.success());
    let authenticated = account.is_some();
    let account_name = account.as_ref().and_then(|value| {
        value
            .pointer("/user/name")
            .and_then(|value| value.as_str())
            .map(str::to_string)
    });
    let detail = if !authenticated {
        Some("Azure CLI is installed but not signed in.".into())
    } else if !extension {
        Some("Install the Azure DevOps CLI extension before creating repositories.".into())
    } else {
        None
    };
    GitProviderProbe {
        provider: "azure_devops".into(),
        display_name: "Azure DevOps".into(),
        installed: true,
        authenticated,
        can_create: authenticated && extension,
        account: account_name,
        host: None,
        detail,
        install_url: "https://learn.microsoft.com/cli/azure/install-azure-cli".into(),
    }
}

fn unavailable_probe(
    provider: &str,
    display_name: &str,
    install_url: &str,
    detail: &str,
) -> GitProviderProbe {
    GitProviderProbe {
        provider: provider.into(),
        display_name: display_name.into(),
        installed: false,
        authenticated: false,
        can_create: false,
        account: None,
        host: None,
        detail: Some(detail.into()),
        install_url: install_url.into(),
    }
}

fn create_or_resume_remote(
    receipts_path: &Path,
    request: &CreateProjectRemoteRequest,
    context: crate::handoff::project::NewRemoteContext,
) -> Result<ProjectRemoteResult, String> {
    let repository = std::fs::canonicalize(&context.repository_path)
        .map_err(|error| format!("resolve local project repository: {error}"))?;
    let repository_text = repository.to_string_lossy().into_owned();
    let mut receipts = load_receipts(receipts_path)?;
    let existing = receipts
        .receipts
        .iter()
        .position(|receipt| receipt.idempotency_key == request.idempotency_key);
    let index = if let Some(index) = existing {
        validate_retry(
            &receipts.receipts[index],
            request,
            &repository_text,
            &context,
        )?;
        index
    } else {
        if !configured_remotes(&repository)?.is_empty() {
            return Err("remote creation is only available for a local repository with no configured remotes".into());
        }
        if lookup_provider_repository(request)?.is_some() {
            return Err(
                "that remote repository already exists; choose another name or add it manually"
                    .into(),
            );
        }
        receipts.receipts.push(RemoteSetupReceipt {
            idempotency_key: request.idempotency_key.clone(),
            project_id: request.project_id.clone(),
            provider: request.provider.clone(),
            namespace: request.namespace.clone(),
            repository_name: request.repository_name.clone(),
            visibility: request.visibility.clone(),
            azure_project: request.azure_project.clone(),
            repository_path: repository_text,
            default_branch: context.default_branch.clone(),
            canonical_commit_id: context.canonical_commit_id.clone(),
            local_commit_id: context.local_commit_id.clone(),
            remote_name: "origin".into(),
            remote_url: None,
            web_url: None,
            status: RemoteSetupStatus::Prepared,
        });
        save_receipts(receipts_path, &receipts)?;
        receipts.receipts.len() - 1
    };

    if receipts.receipts[index].status == RemoteSetupStatus::Completed {
        verify_remote_branch(&repository, &receipts.receipts[index])?;
        return Ok(remote_result(&receipts.receipts[index], false, true));
    }

    let provider_repository = if let Some(configured) = configured_origin(&repository)? {
        let known = lookup_provider_repository(request)?
            .ok_or("the prepared remote repository could not be found through its provider")?;
        if !same_remote(&configured, &known.remote_url) {
            return Err("the local origin does not match the prepared provider repository".into());
        }
        known
    } else if let Some(existing) = lookup_provider_repository(request)? {
        add_origin(&repository, &existing.remote_url)?;
        existing
    } else {
        create_provider_repository(request, &repository, &context)?
    };
    reject_embedded_credentials(&provider_repository.remote_url)?;

    if configured_origin(&repository)?.is_none() {
        add_origin(&repository, &provider_repository.remote_url)?;
    }
    match remote_branch_commit(&repository, "origin", &context.default_branch)? {
        Some(commit) if commit == context.local_commit_id => {}
        Some(_) => {
            return Err(
                "the new remote default branch contains unexpected content; it was not overwritten"
                    .into(),
            )
        }
        None => push_default_branch(&repository, &context.default_branch)?,
    }
    let pushed = remote_branch_commit(&repository, "origin", &context.default_branch)?;
    if pushed.as_deref() != Some(&context.local_commit_id) {
        return Err("the provider repository was created but its default branch could not be confirmed; retry after fixing Git authentication".into());
    }
    receipts.receipts[index].remote_url = Some(
        configured_origin(&repository)?
            .ok_or("the provider repository was created without a local origin")?,
    );
    receipts.receipts[index].web_url = Some(provider_repository.web_url);
    receipts.receipts[index].status = RemoteSetupStatus::Completed;
    save_receipts(receipts_path, &receipts)?;
    Ok(remote_result(&receipts.receipts[index], true, true))
}

fn create_provider_repository(
    request: &CreateProjectRemoteRequest,
    repository: &Path,
    context: &crate::handoff::project::NewRemoteContext,
) -> Result<ProviderRepository, String> {
    match request.provider.as_str() {
        "github" => {
            let cli = find_cli("gh").ok_or("GitHub CLI is not installed")?;
            let full_name = format!("{}/{}", request.namespace, request.repository_name);
            let visibility = format!("--{}", request.visibility);
            let source = repository
                .to_str()
                .ok_or("local repository path is not valid UTF-8")?;
            let output = run(
                &cli,
                &[
                    "repo",
                    "create",
                    &full_name,
                    &visibility,
                    "--source",
                    source,
                    "--remote",
                    "origin",
                    "--push",
                ],
            )?;
            require_success(output, "create and publish GitHub repository")?;
            lookup_provider_repository(request)?.ok_or_else(|| {
                "GitHub created the repository but it could not be read back".to_string()
            })
        }
        "gitlab" => {
            let cli = find_cli("glab").ok_or("GitLab CLI is not installed")?;
            let full_name = format!("{}/{}", request.namespace, request.repository_name);
            let visibility = match request.visibility.as_str() {
                "public" => "--public",
                "private" => "--private",
                "internal" => "--internal",
                _ => return Err("unsupported GitLab visibility".into()),
            };
            let branch = context.default_branch.as_str();
            let output = Command::new(cli)
                .current_dir(repository)
                .env("GLAB_PROMPT_DISABLED", "1")
                .args([
                    "repo",
                    "create",
                    &full_name,
                    visibility,
                    "--defaultBranch",
                    branch,
                    "--remoteName",
                    "origin",
                ])
                .output()
                .map_err(|error| format!("create GitLab repository: {error}"))?;
            require_success(output, "create GitLab repository")?;
            lookup_provider_repository(request)?
                .or_else(|| {
                    configured_origin(repository)
                        .ok()
                        .flatten()
                        .map(|remote_url| ProviderRepository {
                            web_url: format!("https://gitlab.com/{full_name}"),
                            remote_url,
                        })
                })
                .ok_or_else(|| {
                    "GitLab created the repository but it could not be read back".to_string()
                })
        }
        "azure_devops" => {
            let cli = find_cli("az").ok_or("Azure CLI is not installed")?;
            let project = request
                .azure_project
                .as_deref()
                .ok_or("Azure DevOps project is required")?;
            let output = run(
                &cli,
                &[
                    "repos",
                    "create",
                    "--name",
                    &request.repository_name,
                    "--organization",
                    &request.namespace,
                    "--project",
                    project,
                    "--output",
                    "json",
                ],
            )?;
            let output = require_success(output, "create Azure DevOps repository")?;
            parse_azure_repository(&output.stdout)
        }
        _ => Err("unsupported Git provider".into()),
    }
}

fn lookup_provider_repository(
    request: &CreateProjectRemoteRequest,
) -> Result<Option<ProviderRepository>, String> {
    match request.provider.as_str() {
        "github" => {
            let cli = find_cli("gh").ok_or("GitHub CLI is not installed")?;
            let full_name = format!("{}/{}", request.namespace, request.repository_name);
            let output = run(&cli, &["repo", "view", &full_name, "--json", "url,sshUrl"])?;
            if !output.status.success() {
                return Ok(None);
            }
            let value: serde_json::Value = serde_json::from_slice(&output.stdout)
                .map_err(|error| format!("parse GitHub repository: {error}"))?;
            let web_url = value
                .get("url")
                .and_then(|value| value.as_str())
                .ok_or("GitHub repository response has no URL")?
                .to_string();
            let protocol = run_line(&cli, &["config", "get", "git_protocol"])
                .unwrap_or_else(|| "https".into());
            let remote_url = if protocol == "ssh" {
                value
                    .get("sshUrl")
                    .and_then(|value| value.as_str())
                    .map(str::to_string)
                    .unwrap_or_else(|| format!("git@github.com:{full_name}.git"))
            } else {
                format!("{web_url}.git")
            };
            Ok(Some(ProviderRepository {
                remote_url,
                web_url,
            }))
        }
        "gitlab" => {
            let cli = find_cli("glab").ok_or("GitLab CLI is not installed")?;
            let full_name = format!("{}/{}", request.namespace, request.repository_name);
            let output = run(&cli, &["repo", "view", &full_name, "--output", "json"])?;
            if !output.status.success() {
                return Ok(None);
            }
            let value: serde_json::Value = serde_json::from_slice(&output.stdout)
                .map_err(|error| format!("parse GitLab repository: {error}"))?;
            let web_url = json_string(&value, &["web_url", "webUrl"])
                .unwrap_or_else(|| format!("https://gitlab.com/{full_name}"));
            let remote_url = json_string(&value, &["ssh_url_to_repo", "sshUrlToRepo"])
                .or_else(|| json_string(&value, &["http_url_to_repo", "httpUrlToRepo"]))
                .unwrap_or_else(|| format!("{web_url}.git"));
            Ok(Some(ProviderRepository {
                remote_url,
                web_url,
            }))
        }
        "azure_devops" => {
            let cli = find_cli("az").ok_or("Azure CLI is not installed")?;
            let project = request
                .azure_project
                .as_deref()
                .ok_or("Azure DevOps project is required")?;
            let output = run(
                &cli,
                &[
                    "repos",
                    "show",
                    "--repository",
                    &request.repository_name,
                    "--organization",
                    &request.namespace,
                    "--project",
                    project,
                    "--output",
                    "json",
                ],
            )?;
            if !output.status.success() {
                return Ok(None);
            }
            parse_azure_repository(&output.stdout).map(Some)
        }
        _ => Err("unsupported Git provider".into()),
    }
}

fn parse_azure_repository(bytes: &[u8]) -> Result<ProviderRepository, String> {
    let value: serde_json::Value = serde_json::from_slice(bytes)
        .map_err(|error| format!("parse Azure DevOps repository: {error}"))?;
    let remote_url = value
        .get("remoteUrl")
        .and_then(|value| value.as_str())
        .ok_or("Azure DevOps repository response has no remote URL")?
        .to_string();
    let web_url = value
        .get("webUrl")
        .and_then(|value| value.as_str())
        .unwrap_or(&remote_url)
        .to_string();
    Ok(ProviderRepository {
        remote_url,
        web_url,
    })
}

fn validate_request(request: &CreateProjectRemoteRequest) -> Result<(), String> {
    validate_token("project ID", &request.project_id, 256, false)?;
    validate_idempotency_key(&request.idempotency_key)?;
    if !matches!(
        request.provider.as_str(),
        "github" | "gitlab" | "azure_devops"
    ) {
        return Err("unsupported Git provider".into());
    }
    validate_token("repository name", &request.repository_name, 128, false)?;
    if matches!(request.repository_name.as_str(), "." | "..") {
        return Err("repository name contains unsupported characters".into());
    }
    if !matches!(
        request.visibility.as_str(),
        "private" | "public" | "internal"
    ) {
        return Err("repository visibility must be private, public, or internal".into());
    }
    if request.provider == "github" && request.visibility == "internal" {
        // GitHub Enterprise may support this, but the initial github.com adapter
        // deliberately does not guess account capabilities.
        return Err("internal GitHub repositories are not supported by this adapter".into());
    }
    if request.provider == "azure_devops" {
        validate_azure_organization(&request.namespace)?;
        let project = request
            .azure_project
            .as_deref()
            .ok_or("Azure DevOps project is required")?;
        validate_token("Azure DevOps project", project, 256, true)?;
        if !(request.namespace.starts_with("https://dev.azure.com/")
            || request.namespace.starts_with("https://")
            || request.namespace.starts_with("http://"))
        {
            return Err("Azure DevOps organization must be an HTTP(S) URL".into());
        }
    } else {
        validate_namespace(&request.namespace)?;
    }
    Ok(())
}

fn validate_idempotency_key(value: &str) -> Result<(), String> {
    if value.is_empty()
        || value != value.trim()
        || value.len() > 512
        || value.chars().any(char::is_control)
    {
        return Err("Idempotency-Key is required, trimmed, and at most 512 characters".into());
    }
    Ok(())
}

fn validate_retry(
    receipt: &RemoteSetupReceipt,
    request: &CreateProjectRemoteRequest,
    repository_path: &str,
    context: &crate::handoff::project::NewRemoteContext,
) -> Result<(), String> {
    if receipt.project_id != request.project_id
        || receipt.provider != request.provider
        || receipt.namespace != request.namespace
        || receipt.repository_name != request.repository_name
        || receipt.visibility != request.visibility
        || receipt.azure_project != request.azure_project
        || receipt.repository_path != repository_path
        || receipt.default_branch != context.default_branch
        || receipt.canonical_commit_id != context.canonical_commit_id
        || receipt.local_commit_id != context.local_commit_id
    {
        return Err(
            "this Idempotency-Key was already used for different remote setup parameters".into(),
        );
    }
    Ok(())
}

fn validate_namespace(value: &str) -> Result<(), String> {
    if value.is_empty()
        || value.len() > 512
        || value.split('/').any(|segment| {
            segment.is_empty()
                || segment == "."
                || segment == ".."
                || !segment
                    .bytes()
                    .all(|byte| byte.is_ascii_alphanumeric() || matches!(byte, b'.' | b'_' | b'-'))
        })
    {
        return Err("provider namespace contains unsupported characters".into());
    }
    Ok(())
}

fn validate_token(name: &str, value: &str, maximum: usize, spaces: bool) -> Result<(), String> {
    let valid = !value.is_empty()
        && value == value.trim()
        && value.len() <= maximum
        && value.bytes().all(|byte| {
            byte.is_ascii_alphanumeric()
                || matches!(byte, b'.' | b'_' | b'-')
                || (spaces && byte == b' ')
        });
    if valid {
        Ok(())
    } else {
        Err(format!("{name} contains unsupported characters"))
    }
}

fn validate_azure_organization(value: &str) -> Result<(), String> {
    if value.trim() != value || value.contains(['\0', '\r', '\n']) || value.len() > 2048 {
        return Err("Azure DevOps organization URL is invalid".into());
    }
    let url = reqwest::Url::parse(value)
        .map_err(|_| "Azure DevOps organization URL is invalid".to_string())?;
    if !matches!(url.scheme(), "http" | "https")
        || url.host_str().is_none()
        || !url.username().is_empty()
        || url.password().is_some()
    {
        return Err("Azure DevOps organization URL is invalid".into());
    }
    Ok(())
}

fn configured_remotes(repository: &Path) -> Result<Vec<String>, String> {
    let output = git(repository, &["remote"])?;
    require_success(output, "list configured Git remotes").map(|output| {
        String::from_utf8_lossy(&output.stdout)
            .lines()
            .map(str::to_string)
            .collect()
    })
}

fn configured_origin(repository: &Path) -> Result<Option<String>, String> {
    let output = git(repository, &["remote", "get-url", "origin"])?;
    if !output.status.success() {
        return Ok(None);
    }
    let value = String::from_utf8_lossy(&output.stdout).trim().to_string();
    Ok((!value.is_empty()).then_some(value))
}

fn add_origin(repository: &Path, url: &str) -> Result<(), String> {
    reject_embedded_credentials(url)?;
    let output = git(repository, &["remote", "add", "origin", url])?;
    require_success(output, "add provider repository as origin").map(|_| ())
}

fn reject_embedded_credentials(value: &str) -> Result<(), String> {
    if let Ok(url) = reqwest::Url::parse(value) {
        if matches!(url.scheme(), "http" | "https")
            && (!url.username().is_empty() || url.password().is_some())
        {
            return Err(
                "the provider returned a remote URL containing embedded credentials".into(),
            );
        }
    }
    Ok(())
}

fn push_default_branch(repository: &Path, branch: &str) -> Result<(), String> {
    let refspec = format!("HEAD:refs/heads/{branch}");
    let output = git(repository, &["push", "--set-upstream", "origin", &refspec])?;
    require_success(
        output,
        "push the local project to its new provider repository",
    )
    .map(|_| ())
}

fn remote_branch_commit(
    repository: &Path,
    remote: &str,
    branch: &str,
) -> Result<Option<String>, String> {
    let reference = format!("refs/heads/{branch}");
    let output = git(repository, &["ls-remote", "--heads", remote, &reference])?;
    if !output.status.success() {
        return Err(format!(
            "read provider default branch: {}",
            safe_command_detail(&output.stderr)
        ));
    }
    let line = String::from_utf8_lossy(&output.stdout).trim().to_string();
    if line.is_empty() {
        return Ok(None);
    }
    let commit = line
        .split_whitespace()
        .next()
        .ok_or("provider returned an invalid branch identity")?;
    if !matches!(commit.len(), 40 | 64) || !commit.bytes().all(|byte| byte.is_ascii_hexdigit()) {
        return Err("provider returned an invalid branch identity".into());
    }
    Ok(Some(commit.to_ascii_lowercase()))
}

fn verify_remote_branch(repository: &Path, receipt: &RemoteSetupReceipt) -> Result<(), String> {
    let configured = configured_origin(repository)?
        .ok_or("the recorded provider repository is no longer configured as origin")?;
    let expected = receipt
        .remote_url
        .as_deref()
        .ok_or("the completed provider receipt has no remote URL")?;
    if !same_remote(&configured, expected) {
        return Err(
            "the configured origin no longer matches the recorded provider repository".into(),
        );
    }
    let commit = remote_branch_commit(repository, "origin", &receipt.default_branch)?;
    if commit.as_deref() != Some(&receipt.local_commit_id) {
        return Err(
            "the provider default branch no longer points to the published project commit".into(),
        );
    }
    Ok(())
}

fn same_remote(left: &str, right: &str) -> bool {
    normalize_remote(left) == normalize_remote(right)
}

fn normalize_remote(value: &str) -> String {
    let value = value.trim();
    if let Ok(url) = reqwest::Url::parse(value) {
        if let Some(host) = url.host_str() {
            return format!(
                "{}{}",
                host.to_ascii_lowercase(),
                url.path()
                    .trim_end_matches('/')
                    .trim_end_matches(".git")
                    .to_ascii_lowercase()
            );
        }
    }
    if let Some((authority, path)) = value.split_once(':') {
        if authority.contains('@') && !path.contains("//") {
            let host = authority
                .rsplit_once('@')
                .map(|(_, host)| host)
                .unwrap_or(authority);
            return format!(
                "{}/{}",
                host.to_ascii_lowercase(),
                path.trim_start_matches('/')
                    .trim_end_matches('/')
                    .trim_end_matches(".git")
                    .to_ascii_lowercase()
            );
        }
    }
    value
        .trim_end_matches('/')
        .trim_end_matches(".git")
        .to_ascii_lowercase()
}

fn remote_result(receipt: &RemoteSetupReceipt, created: bool, pushed: bool) -> ProjectRemoteResult {
    ProjectRemoteResult {
        project_id: receipt.project_id.clone(),
        provider: receipt.provider.clone(),
        repository_path: receipt.repository_path.clone(),
        remote_name: receipt.remote_name.clone(),
        remote_url: receipt
            .remote_url
            .as_deref()
            .map(display_remote_location)
            .unwrap_or_default(),
        web_url: receipt.web_url.clone().unwrap_or_default(),
        default_branch: receipt.default_branch.clone(),
        local_commit_id: receipt.local_commit_id.clone(),
        created,
        pushed,
    }
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
    }
    value
        .rsplit_once('@')
        .map(|(_, location)| location.to_string())
        .unwrap_or_else(|| value.to_string())
}

pub(crate) fn remote_receipts_path(app: &AppHandle) -> Result<PathBuf, String> {
    let directory = app
        .path()
        .app_data_dir()
        .map_err(|error| format!("resolve app data directory: {error}"))?;
    std::fs::create_dir_all(&directory)
        .map_err(|error| format!("create app data directory: {error}"))?;
    Ok(directory.join(REMOTE_RECEIPTS_FILE))
}

pub(crate) fn created_remote_watermarks(
    path: &Path,
    project_id: &str,
    repository_path: &str,
) -> Result<Vec<CreatedRemoteWatermark>, String> {
    Ok(load_receipts(path)?
        .receipts
        .into_iter()
        .filter(|receipt| {
            receipt.status == RemoteSetupStatus::Completed
                && receipt.project_id == project_id
                && receipt.repository_path == repository_path
        })
        .map(|receipt| CreatedRemoteWatermark {
            remote_name: receipt.remote_name,
            default_branch: receipt.default_branch,
            canonical_commit_id: receipt.canonical_commit_id,
        })
        .collect())
}

pub(crate) fn remove_project_remote_state(app: &AppHandle, project_id: &str) -> Result<(), String> {
    let path = remote_receipts_path(app)?;
    if !path.exists() {
        return Ok(());
    }
    let _guard = REMOTE_SETUP_LOCK
        .lock()
        .map_err(|_| "the project remote setup lock is unavailable".to_string())?;
    let mut receipts = load_receipts(&path)?;
    let before = receipts.receipts.len();
    receipts
        .receipts
        .retain(|receipt| receipt.project_id != project_id);
    if receipts.receipts.len() != before {
        save_receipts(&path, &receipts)?;
    }
    Ok(())
}

fn load_receipts(path: &Path) -> Result<RemoteSetupReceipts, String> {
    match std::fs::read_to_string(path) {
        Ok(contents) => {
            let receipts: RemoteSetupReceipts = serde_json::from_str(&contents)
                .map_err(|error| format!("parse project remote setup receipts: {error}"))?;
            if receipts.version != 1 {
                return Err("project remote setup receipt version is unsupported".into());
            }
            Ok(receipts)
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            Ok(RemoteSetupReceipts::default())
        }
        Err(error) => Err(format!("read project remote setup receipts: {error}")),
    }
}

fn save_receipts(path: &Path, receipts: &RemoteSetupReceipts) -> Result<(), String> {
    let contents = serde_json::to_vec_pretty(receipts)
        .map_err(|error| format!("encode project remote setup receipts: {error}"))?;
    let parent = path
        .parent()
        .ok_or("project remote setup receipt path has no parent")?;
    let mut temporary = tempfile::NamedTempFile::new_in(parent)
        .map_err(|error| format!("create temporary project remote setup receipt: {error}"))?;
    temporary
        .write_all(&contents)
        .and_then(|_| temporary.as_file().sync_all())
        .map_err(|error| format!("write project remote setup receipt: {error}"))?;
    temporary
        .persist(path)
        .map_err(|error| format!("replace project remote setup receipt: {}", error.error))?;
    Ok(())
}

fn find_cli(name: &str) -> Option<PathBuf> {
    let executables = executable_names(name);
    let mut candidates = Vec::new();
    if let Some(path) = std::env::var_os("PATH") {
        for directory in std::env::split_paths(&path) {
            candidates.extend(executables.iter().map(|name| directory.join(name)));
        }
    }
    #[cfg(target_os = "macos")]
    for directory in ["/opt/homebrew/bin", "/usr/local/bin", "/usr/bin"] {
        candidates.extend(
            executables
                .iter()
                .map(|name| Path::new(directory).join(name)),
        );
    }
    #[cfg(target_os = "linux")]
    for directory in ["/usr/local/bin", "/usr/bin", "/snap/bin"] {
        candidates.extend(
            executables
                .iter()
                .map(|name| Path::new(directory).join(name)),
        );
    }
    #[cfg(target_os = "windows")]
    {
        for variable in ["ProgramFiles", "LOCALAPPDATA"] {
            if let Some(root) = std::env::var_os(variable) {
                candidates.extend(
                    executables
                        .iter()
                        .map(|name| PathBuf::from(&root).join("GitHub CLI").join(name)),
                );
            }
        }
    }
    candidates.into_iter().find(|candidate| candidate.is_file())
}

fn executable_names(name: &str) -> Vec<OsString> {
    #[cfg(target_os = "windows")]
    {
        vec![
            OsString::from(format!("{name}.exe")),
            OsString::from(format!("{name}.cmd")),
        ]
    }
    #[cfg(not(target_os = "windows"))]
    {
        vec![OsString::from(name)]
    }
}

fn run(cli: &Path, args: &[&str]) -> Result<Output, String> {
    Command::new(cli)
        .args(args)
        .env("GH_PROMPT_DISABLED", "1")
        .env("GLAB_PROMPT_DISABLED", "1")
        .output()
        .map_err(|error| format!("run {}: {error}", cli.display()))
}

fn run_line(cli: &Path, args: &[&str]) -> Option<String> {
    let output = run(cli, args).ok()?;
    if !output.status.success() {
        return None;
    }
    let value = String::from_utf8_lossy(&output.stdout).trim().to_string();
    (!value.is_empty()).then_some(value)
}

fn run_json(cli: &Path, args: &[&str]) -> Option<serde_json::Value> {
    let output = run(cli, args).ok()?;
    output
        .status
        .success()
        .then(|| serde_json::from_slice(&output.stdout).ok())
        .flatten()
}

fn git(repository: &Path, args: &[&str]) -> Result<Output, String> {
    Command::new("git")
        .arg("-C")
        .arg(repository)
        .args(args)
        .env("GIT_TERMINAL_PROMPT", "0")
        .output()
        .map_err(|error| format!("run Git: {error}"))
}

fn git_output_line(repository: &Path, args: &[&str]) -> Result<String, String> {
    let value = git_output_line_allow_empty(repository, args)?;
    if value.is_empty() {
        Err("Git returned no value".into())
    } else {
        Ok(value)
    }
}

fn git_output_line_allow_empty(repository: &Path, args: &[&str]) -> Result<String, String> {
    let output = require_success(git(repository, args)?, "inspect local project repository")?;
    Ok(String::from_utf8_lossy(&output.stdout).trim().to_string())
}

fn require_success(output: Output, action: &str) -> Result<Output, String> {
    if output.status.success() {
        Ok(output)
    } else {
        Err(format!("{action}: {}", safe_command_detail(&output.stderr)))
    }
}

fn safe_command_detail(bytes: &[u8]) -> String {
    let detail = String::from_utf8_lossy(bytes);
    let lower = detail.to_ascii_lowercase();
    if lower.contains("authorization:")
        || lower.contains("access_token")
        || lower.contains("private-token")
    {
        return "provider command failed; verify authentication and retry".into();
    }
    let trimmed = detail.trim();
    let mut characters = trimmed.chars();
    let shortened: String = characters.by_ref().take(1024).collect();
    if characters.next().is_some() {
        format!("{shortened}…")
    } else {
        shortened
    }
}

fn json_string(value: &serde_json::Value, names: &[&str]) -> Option<String> {
    names.iter().find_map(|name| {
        value
            .get(*name)
            .and_then(|value| value.as_str())
            .map(str::to_string)
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn namespace_validation_accepts_nested_gitlab_groups() {
        assert!(validate_namespace("group/nested-team").is_ok());
        assert!(validate_namespace("group/../other").is_err());
        assert!(validate_namespace("https://github.com/owner").is_err());
    }

    #[test]
    fn azure_organization_must_be_a_credential_free_http_url() {
        assert!(validate_azure_organization("https://dev.azure.com/example").is_ok());
        assert!(validate_azure_organization("https://token@dev.azure.com/example").is_err());
        assert!(validate_azure_organization("file:///tmp/example").is_err());
    }

    #[test]
    fn remote_comparison_ignores_git_suffix_and_case() {
        assert!(same_remote(
            "https://github.com/Owner/Repo.git",
            "https://github.com/owner/repo"
        ));
        assert!(same_remote(
            "git@github.com:Owner/Repo.git",
            "https://github.com/owner/repo"
        ));
    }
}
