//! Trusted execution for approved project-environment changes and isolated
//! validation. Renderer input is limited to an opaque coordinator record ID.

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use tauri::State;

use crate::{docker, profiles};

const ENVIRONMENT_DOCKERFILE: &str = r#"ARG BASE_IMAGE
FROM ${BASE_IMAGE}
USER root
ARG SYSTEM_PACKAGES
RUN apt-get update && \
    apt-get install --yes --no-install-recommends ${SYSTEM_PACKAGES} && \
    rm -rf /var/lib/apt/lists/*
USER commitarium:commitarium
"#;

const VALIDATION_SCRIPT: &str = r#"set -eu
mkdir -p /workspace /tmp/home /tmp/mise-cache /tmp/mise-state
cp -a /source/. /workspace/
git -c safe.directory=/workspace -C /workspace reset --hard "$COMMITARIUM_VALIDATION_COMMIT" >/dev/null
git -c safe.directory=/workspace -C /workspace clean -fd >/dev/null
results='[]'
printf '%s\n' "$results" > /output/results.json
count=$(jq '.commands | length' /spec/job.json)
index=0
while [ "$index" -lt "$count" ]; do
    command=$(jq -r ".commands[$index]" /spec/job.json)
    started=$(date +%s%3N)
    set +e
    timeout 30m /bin/sh -lc "$command" > /tmp/validation-command.log 2>&1
    exit_code=$?
    set -e
    finished=$(date +%s%3N)
    head -c 262144 /tmp/validation-command.log > /tmp/validation-command-bounded.log
    results=$(printf '%s' "$results" | jq \
        --arg command "$command" \
        --argjson exit_code "$exit_code" \
        --argjson duration_ms "$((finished-started))" \
        --rawfile output /tmp/validation-command-bounded.log \
        '. + [{command:$command,exit_code:$exit_code,output:$output,duration_ms:$duration_ms}]')
    printf '%s\n' "$results" > /output/results.json
    if [ "$exit_code" -ne 0 ]; then break; fi
    index=$((index+1))
done
"#;

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct EnvironmentRequest {
    id: String,
    session_id: String,
    system_packages: Vec<String>,
    status: String,
}

#[derive(Deserialize)]
struct ProvisioningSpec {
    request: EnvironmentRequest,
    approved_packages: Vec<String>,
}

#[derive(Debug, Serialize)]
pub struct EnvironmentProvisionResult {
    request: EnvironmentRequest,
    resolved_packages: BTreeMap<String, String>,
    codex_image: String,
    claude_image: String,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ValidationCommandResult {
    command: String,
    exit_code: i32,
    output: String,
    duration_ms: i64,
}

#[derive(Clone, Debug, Deserialize, Serialize)]
pub struct ValidationJob {
    id: String,
    project_id: String,
    workspace_id: String,
    commit_id: String,
    commands: Vec<String>,
    status: String,
    results: Vec<ValidationCommandResult>,
    error: Option<String>,
}

#[derive(Debug, Serialize)]
pub struct ValidationRunResult {
    job: ValidationJob,
}

#[derive(Serialize)]
struct EnvironmentCompletion<'a> {
    resolved_packages: &'a BTreeMap<String, String>,
}

#[derive(Serialize)]
struct FailureBody<'a> {
    reason: &'a str,
}

#[derive(Serialize)]
struct ValidationCompletion<'a> {
    results: &'a [ValidationCommandResult],
    error: &'a str,
}

fn coordinator_url(path: &str) -> String {
    let port = std::env::var("COMMITARIUM_COORDINATOR_PORT").unwrap_or_else(|_| "8080".into());
    format!("http://127.0.0.1:{port}{path}")
}

fn safe_id(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 160
        && value
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || b"._:-".contains(&byte))
}

fn safe_package(value: &str) -> bool {
    !value.is_empty()
        && value.len() <= 128
        && value.bytes().enumerate().all(|(index, byte)| {
            byte.is_ascii_lowercase()
                || byte.is_ascii_digit()
                || (index > 0 && matches!(byte, b'+' | b'.' | b'-'))
        })
}

async fn response_json<T: for<'de> Deserialize<'de>>(
    response: reqwest::Response,
) -> Result<T, String> {
    let status = response.status();
    if !status.is_success() {
        return Err(format!("coordinator returned HTTP {status}"));
    }
    response
        .json::<T>()
        .await
        .map_err(|e| format!("decode coordinator response: {e}"))
}

#[tauri::command]
pub async fn provision_environment_request(
    request_id: String,
    manager: State<'_, profiles::ProfileManager>,
) -> Result<EnvironmentProvisionResult, String> {
    if !safe_id(&request_id) {
        return Err("environment request ID is invalid".into());
    }
    let client = reqwest::Client::new();
    let spec: ProvisioningSpec = response_json(
        client
            .post(coordinator_url(&format!(
                "/api/v1/environment-requests/{request_id}/provision"
            )))
            .body(Vec::new())
            .send()
            .await
            .map_err(|e| format!("begin environment provisioning: {e}"))?,
    )
    .await?;
    if spec.request.id != request_id
        || (spec.request.status != "provisioning" && spec.request.status != "ready")
        || spec.approved_packages.is_empty()
        || spec
            .approved_packages
            .iter()
            .any(|item| !safe_package(item))
    {
        return Err("coordinator returned an unsafe environment specification".into());
    }

    let profile_manager = manager.inner().clone();
    let packages = spec.approved_packages.clone();
    let provisioned =
        tauri::async_runtime::spawn_blocking(move || provision_images(&profile_manager, &packages))
            .await
            .map_err(|_| "environment image provisioning task stopped unexpectedly".to_string())?;

    let (resolved, codex_image, claude_image) = match provisioned {
        Ok(value) => value,
        Err(error) => {
            let detail = bounded_error(&error);
            let _ = client
                .post(coordinator_url(&format!(
                    "/api/v1/environment-requests/{request_id}/fail"
                )))
                .json(&FailureBody { reason: &detail })
                .send()
                .await;
            return Err(error);
        }
    };

    let request: EnvironmentRequest = response_json(
        client
            .post(coordinator_url(&format!(
                "/api/v1/environment-requests/{request_id}/complete"
            )))
            .json(&EnvironmentCompletion {
                resolved_packages: &resolved,
            })
            .send()
            .await
            .map_err(|e| format!("record completed environment provisioning: {e}"))?,
    )
    .await?;
    Ok(EnvironmentProvisionResult {
        request,
        resolved_packages: resolved,
        codex_image,
        claude_image,
    })
}

fn provision_images(
    manager: &profiles::ProfileManager,
    packages: &[String],
) -> Result<(BTreeMap<String, String>, String, String), String> {
    let codex_base = compose_image("codex-worker")?;
    let claude_base = compose_image("claude-worker")?;
    let package_text = packages.join(" ");
    let codex_image = derivative_tag("codex", &codex_base, packages);
    let claude_image = derivative_tag("claude", &claude_base, packages);
    build_derivative(&codex_base, &codex_image, &package_text)?;
    build_derivative(&claude_base, &claude_image, &package_text)?;
    let resolved = resolve_packages(&codex_image, packages)?;
    if resolve_packages(&claude_image, packages)? != resolved {
        return Err("Codex and Claude images resolved different system-package versions".into());
    }
    write_environment_overlay(&codex_image, &claude_image)?;
    docker::reconcile_provider_workers(manager, true)?;
    Ok((resolved, codex_image, claude_image))
}

fn compose_image(service: &str) -> Result<String, String> {
    docker::configured_service_image(service)
}

fn derivative_tag(provider: &str, base: &str, packages: &[String]) -> String {
    let mut digest = Sha256::new();
    digest.update(base.as_bytes());
    for package in packages {
        digest.update([0]);
        digest.update(package.as_bytes());
    }
    let encoded = format!("{:x}", digest.finalize());
    format!("commitarium-{provider}-environment:{}", &encoded[..16])
}

fn build_derivative(base: &str, tag: &str, packages: &str) -> Result<(), String> {
    let directory =
        tempfile::tempdir().map_err(|e| format!("create environment build directory: {e}"))?;
    fs::write(directory.path().join("Dockerfile"), ENVIRONMENT_DOCKERFILE)
        .map_err(|e| format!("write environment Dockerfile: {e}"))?;
    let output = docker::docker_command()
        .args(["build", "--pull=false", "--build-arg"])
        .arg(format!("BASE_IMAGE={base}"))
        .args(["--build-arg"])
        .arg(format!("SYSTEM_PACKAGES={packages}"))
        .args(["--tag", tag, "--file"])
        .arg(directory.path().join("Dockerfile"))
        .arg(directory.path())
        .output()
        .map_err(|e| format!("build managed environment image: {e}"))?;
    if !output.status.success() {
        return Err(format!(
            "build managed environment image: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    Ok(())
}

fn resolve_packages(image: &str, packages: &[String]) -> Result<BTreeMap<String, String>, String> {
    let mut command = docker::docker_command();
    command.args([
        "run",
        "--rm",
        "--entrypoint",
        "/bin/sh",
        image,
        "-c",
        r#"for package do printf '%s\t' "$package"; dpkg-query -W -f='${Version}\n' "$package"; done"#,
        "commitarium-package-query",
    ]);
    for package in packages {
        command.arg(package);
    }
    let output = command
        .output()
        .map_err(|e| format!("inspect managed environment packages: {e}"))?;
    if !output.status.success() {
        return Err("could not verify installed system-package versions".into());
    }
    let mut resolved = BTreeMap::new();
    for line in String::from_utf8_lossy(&output.stdout).lines() {
        if let Some((name, version)) = line.split_once('\t') {
            resolved.insert(name.to_string(), version.to_string());
        }
    }
    for package in packages {
        if !resolved.contains_key(package) {
            return Err(format!(
                "installed package {package} did not report a version"
            ));
        }
    }
    Ok(resolved)
}

fn write_environment_overlay(codex: &str, claude: &str) -> Result<(), String> {
    let path = docker::environment_compose_file()?;
    let parent = path
        .parent()
        .ok_or_else(|| "environment Compose path has no parent".to_string())?;
    fs::create_dir_all(parent).map_err(|e| format!("create environment Compose directory: {e}"))?;
    let contents = format!(
        r#"services:
  codex-worker:
    build: !reset null
    image: {codex}
  codex-reviewer-worker:
    build: !reset null
    image: {codex}
  claude-worker:
    build: !reset null
    image: {claude}
  claude-reviewer-worker:
    build: !reset null
    image: {claude}
"#
    );
    let mut temporary = tempfile::NamedTempFile::new_in(parent)
        .map_err(|e| format!("create environment Compose update: {e}"))?;
    temporary
        .write_all(contents.as_bytes())
        .map_err(|e| format!("write environment Compose update: {e}"))?;
    temporary
        .persist(&path)
        .map_err(|e| format!("install environment Compose update: {e}"))?;
    Ok(())
}

#[tauri::command]
pub async fn run_validation_job(job_id: String) -> Result<ValidationRunResult, String> {
    if !safe_id(&job_id) {
        return Err("validation job ID is invalid".into());
    }
    let client = reqwest::Client::new();
    let job: ValidationJob = response_json(
        client
            .post(coordinator_url(&format!(
                "/api/v1/validation-jobs/{job_id}/claim"
            )))
            .body(Vec::new())
            .send()
            .await
            .map_err(|e| format!("claim validation job: {e}"))?,
    )
    .await?;
    if job.id != job_id
        || (job.status != "running" && job.status != "passed" && job.status != "failed")
        || !safe_id(&job.project_id)
        || !safe_id(&job.workspace_id)
        || !safe_commit(&job.commit_id)
        || job.commands.is_empty()
    {
        return Err("coordinator returned an unsafe validation job".into());
    }
    let (results, error) = if job.status == "running" {
        let run_job = job.clone();
        let outcome = tauri::async_runtime::spawn_blocking(move || execute_validation(&run_job))
            .await
            .map_err(|_| "validation task stopped unexpectedly".to_string())?;
        match outcome {
            Ok(results) => (results, String::new()),
            Err(error) => (Vec::new(), bounded_error(&error)),
        }
    } else {
        (job.results.clone(), job.error.clone().unwrap_or_default())
    };
    let completed: ValidationJob = response_json(
        client
            .post(coordinator_url(&format!(
                "/api/v1/validation-jobs/{job_id}/complete"
            )))
            .json(&ValidationCompletion {
                results: &results,
                error: &error,
            })
            .send()
            .await
            .map_err(|e| format!("record validation result: {e}"))?,
    )
    .await?;
    Ok(ValidationRunResult { job: completed })
}

fn safe_commit(value: &str) -> bool {
    (value.len() == 40 || value.len() == 64)
        && value
            .bytes()
            .all(|byte| byte.is_ascii_hexdigit() && !byte.is_ascii_uppercase())
}

fn execute_validation(job: &ValidationJob) -> Result<Vec<ValidationCommandResult>, String> {
    let workspace = managed_workspace(&job.workspace_id)?;
    let image = compose_image("codex-worker")?;
    let spec_dir =
        tempfile::tempdir().map_err(|e| format!("create validation specification: {e}"))?;
    let output_dir =
        tempfile::tempdir().map_err(|e| format!("create validation result directory: {e}"))?;
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(output_dir.path(), fs::Permissions::from_mode(0o777))
            .map_err(|e| format!("prepare validation result directory: {e}"))?;
    }
    fs::write(
        spec_dir.path().join("job.json"),
        serde_json::to_vec(job).map_err(|e| e.to_string())?,
    )
    .map_err(|e| format!("write validation specification: {e}"))?;
    let toolchain_volume = format!("{}_commitarium-toolchains", docker::PROJECT_NAME);
    let mut command = docker::docker_command();
    command
        .args([
            "run",
            "--rm",
            "--network",
            "none",
            "--cpus",
            "2",
            "--memory",
            "4g",
            "--pids-limit",
            "512",
            "--read-only",
        ])
        .args([
            "--tmpfs",
            "/workspace:rw,exec,nosuid,nodev,size=4g,uid=65532,gid=65532",
        ])
        .args([
            "--tmpfs",
            "/tmp:rw,exec,nosuid,nodev,size=1g,uid=65532,gid=65532",
        ])
        .arg("--mount")
        .arg(format!(
            "type=bind,src={},dst=/source,readonly",
            workspace.display()
        ))
        .arg("--mount")
        .arg(format!(
            "type=bind,src={},dst=/spec,readonly",
            spec_dir.path().display()
        ))
        .arg("--mount")
        .arg(format!(
            "type=bind,src={},dst=/output",
            output_dir.path().display()
        ))
        .arg("--mount")
        .arg(format!(
            "type=volume,src={toolchain_volume},dst=/toolchains,readonly"
        ))
        .arg("--env")
        .arg(format!("COMMITARIUM_VALIDATION_COMMIT={}", job.commit_id))
        .args([
            "--env",
            "HOME=/tmp/home",
            "--env",
            "PATH=/toolchains/data/shims:/usr/local/bin:/usr/bin:/bin",
        ])
        .arg("--env")
        .arg(format!(
            "MISE_GLOBAL_CONFIG_FILE=/toolchains/projects/{}/mise.toml",
            job.project_id
        ));
    command
        .args([
            "--env",
            "MISE_SAFE=1",
            "--env",
            "MISE_DATA_DIR=/toolchains/data",
            "--env",
            "MISE_CACHE_DIR=/tmp/mise-cache",
            "--env",
            "MISE_STATE_DIR=/tmp/mise-state",
        ])
        .args([
            "--cap-drop",
            "ALL",
            "--security-opt",
            "no-new-privileges",
            "--entrypoint",
            "/usr/bin/timeout",
            &image,
            "45m",
            "/bin/sh",
            "-c",
            VALIDATION_SCRIPT,
        ]);
    let output = command
        .output()
        .map_err(|e| format!("launch isolated validation: {e}"))?;
    if !output.status.success() {
        return Err(format!(
            "isolated validation container failed: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    let contents = fs::read(output_dir.path().join("results.json"))
        .map_err(|e| format!("read isolated validation results: {e}"))?;
    serde_json::from_slice(&contents)
        .map_err(|e| format!("decode isolated validation results: {e}"))
}

fn managed_workspace(workspace_id: &str) -> Result<PathBuf, String> {
    let source = std::env::var_os("COMMITARIUM_WORKSPACE_SOURCE")
        .map(PathBuf::from)
        .unwrap_or_else(|| {
            docker::compose_file()
                .unwrap_or_else(|_| PathBuf::from("compose.yml"))
                .parent()
                .unwrap_or(Path::new("."))
                .join(".commitarium/workspaces")
        });
    let root =
        fs::canonicalize(&source).map_err(|e| format!("resolve managed workspace root: {e}"))?;
    let workspace = fs::canonicalize(root.join(workspace_id))
        .map_err(|e| format!("resolve managed validation workspace: {e}"))?;
    if workspace.parent() != Some(root.as_path()) || !workspace.is_dir() {
        return Err("managed validation workspace is outside its root".into());
    }
    Ok(workspace)
}

fn bounded_error(error: &str) -> String {
    error.chars().take(1000).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn renderer_ids_are_opaque_but_strictly_bounded() {
        assert!(safe_id("val_ab12:retry-1"));
        assert!(!safe_id("../workspace"));
        assert!(!safe_id("contains a space"));
        assert!(!safe_id(&"a".repeat(161)));
    }

    #[test]
    fn package_names_match_the_coordinator_contract() {
        assert!(safe_package("libvips-dev"));
        assert!(safe_package("libstdc++6"));
        assert!(!safe_package("LibVips"));
        assert!(!safe_package("libvips;id"));
        assert!(!safe_package("-option"));
    }

    #[test]
    fn validation_accepts_only_full_lowercase_object_ids() {
        assert!(safe_commit("0123456789abcdef0123456789abcdef01234567"));
        assert!(safe_commit(&"a".repeat(64)));
        assert!(!safe_commit("deadbeef"));
        assert!(!safe_commit("0123456789ABCDEF0123456789ABCDEF01234567"));
    }

    #[test]
    fn derivative_tags_are_stable_and_environment_specific() {
        let first = derivative_tag("codex", "sha256:base", &["git-lfs".into()]);
        assert_eq!(
            first,
            derivative_tag("codex", "sha256:base", &["git-lfs".into()])
        );
        assert_ne!(
            first,
            derivative_tag("codex", "sha256:base", &["libvips-dev".into()])
        );
    }
}
