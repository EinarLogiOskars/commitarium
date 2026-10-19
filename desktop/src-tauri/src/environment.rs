//! Trusted execution for approved project-environment changes and isolated
//! validation. Renderer input is limited to an opaque coordinator record ID.

use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};
use std::sync::{Mutex, MutexGuard};
use std::time::{Duration, Instant};
use tauri::State;

use crate::agents::Provider;
use crate::{docker, profiles};

/// The ID of the base worker image a derivative was built from.
pub(crate) const BASE_IMAGE_LABEL: &str = "commitarium.base-image";

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
if [ -n "${COMMITARIUM_TEST_COMMIT:-}" ]; then
    git -c safe.directory='*' -C /workspace fetch --quiet --no-tags /tests HEAD '+refs/heads/*:refs/remotes/acceptance/*'
    git -c safe.directory='*' -C /workspace cat-file -e "$COMMITARIUM_TEST_COMMIT^{commit}"
    git -c safe.directory='*' -c user.name=Commitarium -c user.email=commitarium@commitarium.local \
        -C /workspace merge --quiet --no-edit "$COMMITARIUM_TEST_COMMIT"
fi
# Commands run from the repository root, as they do for the agents.
cd /workspace
# The project's dependencies, installed while the container is still online:
# the agents install theirs outside the repository, so a copy has none.
cache=/workspace/.commitarium-cache
export npm_config_cache="$cache/npm" GOMODCACHE="$cache/gomod" GOCACHE="$cache/gobuild" GOTOOLCHAIN=local
install_dependencies() {
    if ls requirements*.txt pyproject.toml setup.py >/dev/null 2>&1; then
        python3 -m venv /workspace/.commitarium-venv
        for file in requirements*.txt; do
            [ -f "$file" ] && /workspace/.commitarium-venv/bin/pip install --quiet --disable-pip-version-check -r "$file"
        done
        if [ -f pyproject.toml ] || [ -f setup.py ]; then
            /workspace/.commitarium-venv/bin/pip install --quiet --disable-pip-version-check -e '.[test,tests,dev]' ||
                /workspace/.commitarium-venv/bin/pip install --quiet --disable-pip-version-check -e . ||
                echo "(the package itself could not be installed; its requirements are)"
        fi
    fi
    if [ -f package.json ] && [ ! -d node_modules ]; then
        if [ -f pnpm-lock.yaml ]; then pnpm install --frozen-lockfile || corepack pnpm install --frozen-lockfile
        elif [ -f yarn.lock ]; then yarn install --frozen-lockfile || corepack yarn install --frozen-lockfile
        elif [ -f package-lock.json ]; then npm ci
        else npm install
        fi
    fi
    if [ -f go.mod ]; then go mod download; fi
}
set +e
install_dependencies > /tmp/install.log 2>&1
installed=$?
set -e
if [ "$installed" -ne 0 ]; then
    tail -c 4000 /tmp/install.log > /output/install-error
    exit 0
fi
if [ -d /workspace/.commitarium-venv ]; then
    export VIRTUAL_ENV=/workspace/.commitarium-venv PATH="/workspace/.commitarium-venv/bin:$PATH"
fi
# The desktop takes the container offline, then says so; the tests run only
# once no route out is left.
: > /output/installed
waited=0
while [ ! -f /output/offline ]; do
    waited=$((waited+1))
    if [ "$waited" -gt 600 ]; then echo "the desktop never took validation offline" >&2; exit 3; fi
    sleep 0.2
done
if awk 'NR > 1 && $2 == "00000000"' /proc/net/route | grep -q .; then
    echo "validation is still online; refusing to run the tests" >&2
    exit 4
fi
results='[]'
printf '%s\n' "$results" > /output/results.json
count=$(jq '.commands | length' /spec/job.json)
index=0
while [ "$index" -lt "$count" ]; do
    command=$(jq -r ".commands[$index]" /spec/job.json)
    started=$(date +%s%3N)
    set +e
    # Not a login shell: /etc/profile would reset PATH to the image's own
    # tools, past the project's toolchain and its dependencies.
    timeout 30m /bin/sh -c "$command" > /tmp/validation-command.log 2>&1
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
    if [ "$exit_code" -ne 0 ] && [ -z "${COMMITARIUM_RUN_ALL:-}" ]; then break; fi
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
    /// "validation", or "acceptance" for the coordinator's own run of the
    /// reviewer's acceptance tests.
    #[serde(default)]
    kind: String,
    /// The reviewer's private test commit, merged in from its checkout.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    merge_commit_id: String,
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

pub(crate) fn coordinator_url(path: &str) -> String {
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
    let _images = environment_images_lock();
    let built = build_environment_images(packages)?;
    docker::reconcile_agent_workers(manager, true)?;
    Ok(built)
}

/// Build a derivative image for each provider whose base worker image is
/// here, check they installed the same versions, and point the environment
/// overlay at them. The workers pick them up on their next reconcile. A new
/// install has no agents (ADR-027), and a provider without agents may never
/// have had its base pulled; it is left out until one of its agents brings
/// the base, and the background refresh then builds it. The images are
/// returned per provider, empty when left out.
fn build_environment_images(
    packages: &[String],
) -> Result<(BTreeMap<String, String>, String, String), String> {
    let package_text = packages.join(" ");
    let mut built: Vec<(Provider, String)> = Vec::new();
    for provider in [Provider::Codex, Provider::Claude] {
        let base = compose_image(provider.template_service())?;
        if inspect_image(&base)?.is_none() {
            continue;
        }
        let image = derivative_tag(provider.executable(), &base, packages);
        build_derivative(&base, &image, &package_text)?;
        built.push((provider, image));
    }
    let Some((_, first)) = built.first() else {
        return Err(
            "no agent worker image is here yet; add and connect an agent, then try again".into(),
        );
    };
    let resolved = resolve_packages(first, packages)?;
    for (_, image) in &built[1..] {
        if resolve_packages(image, packages)? != resolved {
            return Err(
                "Codex and Claude images resolved different system-package versions".into(),
            );
        }
    }
    write_environment_overlay(&built)?;
    let image_of = |wanted: Provider| {
        built
            .iter()
            .find(|(provider, _)| *provider == wanted)
            .map(|(_, image)| image.clone())
            .unwrap_or_default()
    };
    Ok((
        resolved,
        image_of(Provider::Codex),
        image_of(Provider::Claude),
    ))
}

/// Provisioning and the background refresh both build the derivative images,
/// rewrite the overlay, and recreate the workers; they take turns.
pub(crate) fn environment_images_lock() -> MutexGuard<'static, ()> {
    static IMAGES: Mutex<()> = Mutex::new(());
    IMAGES
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
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
    // The tag follows the base's reference, which a rebuilt or updated base
    // keeps; the label records which image it actually was.
    let base_id = inspect_image(base)?
        .ok_or_else(|| format!("the base worker image {base} is missing"))?
        .id;
    let directory =
        tempfile::tempdir().map_err(|e| format!("create environment build directory: {e}"))?;
    fs::write(directory.path().join("Dockerfile"), ENVIRONMENT_DOCKERFILE)
        .map_err(|e| format!("write environment Dockerfile: {e}"))?;
    let output = docker::docker_command()
        .args(["build", "--pull=false", "--build-arg"])
        .arg(format!("BASE_IMAGE={base}"))
        .args(["--build-arg"])
        .arg(format!("SYSTEM_PACKAGES={packages}"))
        .arg("--label")
        .arg(format!("{BASE_IMAGE_LABEL}={base_id}"))
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

fn write_environment_overlay(images: &[(Provider, String)]) -> Result<(), String> {
    let path = docker::environment_compose_file()?;
    let parent = path
        .parent()
        .ok_or_else(|| "environment Compose path has no parent".to_string())?;
    fs::create_dir_all(parent).map_err(|e| format!("create environment Compose directory: {e}"))?;
    // The agent worker templates carry the images; agent workers generated
    // from them pick the approved environment up on the next reconcile.
    let contents = environment_overlay(images);
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

/// Environment overlays written before agents (ADR-016) set the images of the
/// four role workers. Move those images onto the agent worker templates once,
/// so the approved environment survives the upgrade and no stray role
/// services remain.
pub(crate) fn migrate_environment_overlay() -> Result<(), String> {
    let path = docker::environment_compose_file()?;
    let Ok(contents) = fs::read_to_string(&path) else {
        return Ok(());
    };
    if !contents
        .lines()
        .any(|line| line.trim_end() == "  codex-worker:")
    {
        return Ok(());
    }
    match (
        overlay_image(&contents, "codex-worker"),
        overlay_image(&contents, "claude-worker"),
    ) {
        (Some(codex), Some(claude)) => {
            write_environment_overlay(&[(Provider::Codex, codex), (Provider::Claude, claude)])
        }
        _ => Err("the approved-environment overlay has an unexpected form".into()),
    }
}

/// The derivative images the environment overlay selects, none without an
/// approved environment.
pub(crate) fn approved_environment_images() -> Result<Vec<String>, String> {
    let contents = match fs::read_to_string(docker::environment_compose_file()?) {
        Ok(contents) => contents,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(error) => return Err(format!("read the environment overlay: {error}")),
    };
    // A legacy overlay names the role workers until it is migrated.
    Ok([
        Provider::Codex.template_service(),
        Provider::Claude.template_service(),
        "codex-worker",
        "claude-worker",
    ]
    .into_iter()
    .filter_map(|service| overlay_image(&contents, service))
    .collect())
}

/// The overlay pointing each built provider's worker template at its
/// derivative image.
fn environment_overlay(images: &[(Provider, String)]) -> String {
    let mut contents = String::from("services:\n");
    for (provider, image) in images {
        contents.push_str(&format!(
            "  {}:\n    build: !reset null\n    image: {image}\n",
            provider.template_service()
        ));
    }
    contents
}

/// The image an environment overlay (current or legacy) sets for `service`.
fn overlay_image(contents: &str, service: &str) -> Option<String> {
    let header = format!("  {service}:");
    let mut lines = contents
        .lines()
        .skip_while(|line| line.trim_end() != header);
    lines.next()?;
    lines
        .take_while(|line| line.starts_with("    "))
        .find_map(|line| line.trim().strip_prefix("image:"))
        .map(|image| image.trim().to_string())
        .filter(|image| {
            !image.is_empty()
                && image.bytes().all(|byte| {
                    byte.is_ascii_alphanumeric()
                        || matches!(byte, b':' | b'-' | b'_' | b'.' | b'/' | b'@')
                })
        })
}

// --- Keeping the approved environment on the current base worker images ---
//
// A derivative image is built FROM its provider's base worker image as it was
// then. When the base changes (rebuilt in development, pulled by a stack
// update), the workers would keep running the old worker code until the next
// approved request. The CLI updater's background task (cli_updates.rs) checks
// this alongside the CLIs: it rebuilds stale derivatives with the approved
// package union and recreates the workers once none is running a turn.

/// One provider's environment images, as Docker has them now.
#[derive(Debug, Default)]
pub(crate) struct EnvironmentImages {
    /// The ID of the provider's base worker image, unless it is missing.
    base: Option<String>,
    /// The ID of the derivative image the overlay selects, unless missing.
    derivative: Option<String>,
    /// The base image ID recorded on the derivative; derivatives built
    /// before the label have none.
    built_from: Option<String>,
    /// The image IDs of the provider's running workers.
    workers: Vec<String>,
}

#[derive(Debug, Eq, PartialEq)]
pub(crate) enum EnvironmentPlan {
    /// No approved environment, or it and its workers follow the bases.
    Current,
    /// A derivative was built from another base image (or is gone).
    Rebuild,
    /// The derivatives are current, but a worker still runs an older image.
    Recreate,
}

/// What the approved environment needs; `images` is None without one.
pub(crate) fn environment_plan(images: Option<&[EnvironmentImages]>) -> EnvironmentPlan {
    let Some(images) = images else {
        return EnvironmentPlan::Current;
    };
    // A missing base cannot be built from; bringing the stack up restores it.
    if images.iter().any(|provider| {
        provider.base.is_some()
            && (provider.derivative.is_none() || provider.built_from != provider.base)
    }) {
        return EnvironmentPlan::Rebuild;
    }
    if images.iter().any(|provider| {
        provider
            .derivative
            .as_ref()
            .is_some_and(|derivative| provider.workers.iter().any(|image| image != derivative))
    }) {
        return EnvironmentPlan::Recreate;
    }
    EnvironmentPlan::Current
}

/// Inspect each provider's environment images, given its running worker
/// containers. None when no environment has been approved.
pub(crate) fn inspect_environment(
    workers: &[(Provider, Vec<String>)],
) -> Result<Option<Vec<EnvironmentImages>>, String> {
    let Ok(contents) = fs::read_to_string(docker::environment_compose_file()?) else {
        return Ok(None);
    };
    let mut images = Vec::new();
    for (provider, containers) in workers {
        let service = provider.template_service();
        // A provider the overlay leaves out had no base when it was built;
        // with its base here now, the plan rebuilds it.
        let base = inspect_image(&compose_image(service)?)?;
        let derivative = match overlay_image(&contents, service) {
            Some(derivative) => inspect_image(&derivative)?,
            None => None,
        };
        let mut running = Vec::new();
        for container in containers {
            running.extend(container_image(container)?);
        }
        images.push(EnvironmentImages {
            base: base.map(|image| image.id),
            built_from: derivative
                .as_ref()
                .and_then(|image| image.built_from.clone()),
            derivative: derivative.map(|image| image.id),
            workers: running,
        });
    }
    Ok(Some(images))
}

/// The approved package union, read without touching any request.
pub(crate) async fn approved_packages() -> Result<Vec<String>, String> {
    #[derive(Deserialize)]
    struct Approved {
        approved_packages: Vec<String>,
    }
    let approved: Approved = response_json(
        reqwest::Client::new()
            .get(coordinator_url("/api/v1/environment/approved-packages"))
            .send()
            .await
            .map_err(|e| format!("read the approved system packages: {e}"))?,
    )
    .await?;
    if approved.approved_packages.is_empty()
        || !approved
            .approved_packages
            .iter()
            .all(|item| safe_package(item))
    {
        return Err("coordinator returned an unsafe approved package list".into());
    }
    Ok(approved.approved_packages)
}

/// Rebuild both derivatives from the current base images and rewrite the
/// overlay. Running workers keep their image until they are recreated.
pub(crate) fn rebuild_environment_images(packages: &[String]) -> Result<(), String> {
    let _images = environment_images_lock();
    build_environment_images(packages).map(|_| ())
}

/// Recreate the workers onto the current derivative images.
pub(crate) fn recreate_environment_workers(
    manager: &profiles::ProfileManager,
) -> Result<(), String> {
    let _images = environment_images_lock();
    docker::reconcile_agent_workers(manager, true)
}

struct ImageInfo {
    id: String,
    built_from: Option<String>,
}

/// The image's ID and recorded base, or None when Docker has no such image.
fn inspect_image(reference: &str) -> Result<Option<ImageInfo>, String> {
    let output = docker::docker_command()
        .args([
            "image",
            "inspect",
            "--format",
            "{{json .Id}} {{json .Config.Labels}}",
            reference,
        ])
        .output()
        .map_err(|e| format!("inspect image {reference}: {e}"))?;
    if !output.status.success() {
        let stderr = String::from_utf8_lossy(&output.stderr);
        if stderr.contains("No such image") {
            return Ok(None);
        }
        return Err(format!("inspect image {reference}: {}", stderr.trim()));
    }
    image_info(&String::from_utf8_lossy(&output.stdout))
        .map(Some)
        .ok_or_else(|| format!("inspect image {reference}: unexpected answer"))
}

/// Parse `"<id>" <labels JSON or null>`.
fn image_info(output: &str) -> Option<ImageInfo> {
    let (id, labels) = output.trim().split_once(' ')?;
    let id: String = serde_json::from_str(id).ok()?;
    let labels: Option<BTreeMap<String, String>> = serde_json::from_str(labels).ok()?;
    let built_from = labels
        .and_then(|mut labels| labels.remove(BASE_IMAGE_LABEL))
        .filter(|base| !base.is_empty());
    (!id.is_empty()).then_some(ImageInfo { id, built_from })
}

/// The image ID a container runs, unless the container is gone.
fn container_image(container: &str) -> Result<Option<String>, String> {
    let output = docker::docker_command()
        .args(["inspect", "--format", "{{.Image}}", container])
        .output()
        .map_err(|e| format!("inspect a worker: {e}"))?;
    let image = String::from_utf8_lossy(&output.stdout).trim().to_string();
    Ok((output.status.success() && !image.is_empty()).then_some(image))
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
    let image = compose_image("codex-agent-template")?;
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
            "--detach",
            // Online only to install the project's dependencies: the desktop
            // disconnects it before any test runs (wait_offline).
            "--network",
            VALIDATION_NETWORK,
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
        .arg(format!("COMMITARIUM_VALIDATION_COMMIT={}", job.commit_id));
    // An acceptance run merges the reviewer's private test commit, read from
    // its checkout, and runs every test whatever the others did.
    if job.kind == "acceptance" {
        if !safe_commit(&job.merge_commit_id) {
            return Err("the acceptance test commit is invalid".into());
        }
        let tests = reviewer_workspace(&job.workspace_id)?;
        command
            .arg("--mount")
            .arg(format!(
                "type=bind,src={},dst=/tests,readonly",
                tests.display()
            ))
            .arg("--env")
            .arg(format!("COMMITARIUM_TEST_COMMIT={}", job.merge_commit_id))
            .args(["--env", "COMMITARIUM_RUN_ALL=1"]);
    }
    command
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
            "isolated validation container failed to start: {}",
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    let container = ValidationContainer::new(&String::from_utf8_lossy(&output.stdout))?;
    install_then_go_offline(&container, output_dir.path())?;
    let exit_code = container.wait()?;
    if exit_code != 0 {
        return Err(format!(
            "isolated validation container failed: {}",
            container.log_tail()
        ));
    }
    let contents = fs::read(output_dir.path().join("results.json"))
        .map_err(|e| format!("read isolated validation results: {e}"))?;
    let results: Vec<ValidationCommandResult> = serde_json::from_slice(&contents)
        .map_err(|e| format!("decode isolated validation results: {e}"))?;
    if let Some(missing) = missing_dependency(&results, &workspace) {
        return Err(format!(
            "Commitarium could not run the tests: the project's environment lacks {missing}"
        ));
    }
    Ok(results)
}

/// The Docker network a validation run starts on, to install dependencies.
const VALIDATION_NETWORK: &str = "bridge";
/// How long installing the project's dependencies may take.
const INSTALL_TIMEOUT: Duration = Duration::from_secs(20 * 60);

/// A detached validation container, removed when dropped.
struct ValidationContainer {
    id: String,
}

impl ValidationContainer {
    fn new(stdout: &str) -> Result<Self, String> {
        let id = stdout.trim().to_string();
        if id.is_empty() || !id.bytes().all(|b| b.is_ascii_hexdigit()) {
            return Err("isolated validation container has no valid ID".into());
        }
        Ok(Self { id })
    }

    fn running(&self) -> bool {
        docker::docker_command()
            .args(["inspect", "--format", "{{.State.Running}}", &self.id])
            .output()
            .map(|o| String::from_utf8_lossy(&o.stdout).trim() == "true")
            .unwrap_or(false)
    }

    fn wait(&self) -> Result<i32, String> {
        let output = docker::docker_command()
            .args(["wait", &self.id])
            .output()
            .map_err(|e| format!("wait for isolated validation: {e}"))?;
        String::from_utf8_lossy(&output.stdout)
            .trim()
            .parse()
            .map_err(|_| {
                format!(
                    "isolated validation ended without an exit code: {}",
                    self.log_tail()
                )
            })
    }

    fn log_tail(&self) -> String {
        docker::docker_command()
            .args(["logs", "--tail", "40", &self.id])
            .output()
            .map(|o| {
                let mut text = String::from_utf8_lossy(&o.stdout).to_string();
                text.push_str(&String::from_utf8_lossy(&o.stderr));
                text.trim().to_string()
            })
            .unwrap_or_default()
    }
}

impl Drop for ValidationContainer {
    fn drop(&mut self) {
        let _ = docker::docker_command()
            .args(["rm", "--force", &self.id])
            .output();
    }
}

/// Waits while the container installs the project's dependencies, then
/// takes it offline and tells it so; it runs no test while a route out is
/// left. An install that fails ends the run: the tests could not run.
fn install_then_go_offline(container: &ValidationContainer, output: &Path) -> Result<(), String> {
    let started = Instant::now();
    loop {
        if let Ok(log) = fs::read_to_string(output.join("install-error")) {
            return Err(format!(
                "Commitarium could not install the project's dependencies to run the tests:\n{}",
                log.trim()
            ));
        }
        if output.join("installed").exists() {
            break;
        }
        if !container.running() {
            return Err(format!(
                "isolated validation stopped before its tests: {}",
                container.log_tail()
            ));
        }
        if started.elapsed() > INSTALL_TIMEOUT {
            return Err("installing the project's dependencies took longer than 20 minutes".into());
        }
        std::thread::sleep(Duration::from_millis(500));
    }
    let disconnected = docker::docker_command()
        .args([
            "network",
            "disconnect",
            "--force",
            VALIDATION_NETWORK,
            &container.id,
        ])
        .output()
        .map_err(|e| format!("take isolated validation offline: {e}"))?;
    if !disconnected.status.success() {
        return Err(format!(
            "could not take isolated validation offline: {}",
            String::from_utf8_lossy(&disconnected.stderr).trim()
        ));
    }
    fs::write(output.join("offline"), b"")
        .map_err(|e| format!("tell isolated validation it is offline: {e}"))
}

/// What the environment lacks when every command failed on something from
/// outside the project: a Python module, Node package, or command the
/// repository does not hold. A missing module of the project itself is a
/// real failure, never this.
fn missing_dependency(results: &[ValidationCommandResult], repository: &Path) -> Option<String> {
    if results.is_empty() || results.iter().any(|r| r.exit_code == 0) {
        return None;
    }
    let mut first = None;
    for result in results {
        let missing = result.output.lines().find_map(missing_name)?;
        let top = missing
            .split(['.', '/'])
            .find(|part| !part.is_empty() && *part != "@")
            .unwrap_or(&missing);
        let inside = repository.join(top).exists()
            || repository.join(format!("{top}.py")).exists()
            || missing.starts_with('.');
        if inside {
            return None;
        }
        first.get_or_insert(missing);
    }
    first
}

/// The module, package, or command a line of output says is missing.
fn missing_name(line: &str) -> Option<String> {
    let quoted = |text: &str| -> Option<String> {
        let text = text.trim();
        let quote = text.chars().next().filter(|c| matches!(c, '\'' | '"'))?;
        let rest = &text[1..];
        Some(rest[..rest.find(quote)?].to_string())
    };
    if let Some((_, rest)) = line.split_once("No module named ") {
        return quoted(rest).or_else(|| rest.split_whitespace().next().map(str::to_string));
    }
    if let Some((_, rest)) = line.split_once("Cannot find module ") {
        return quoted(rest);
    }
    if let Some((_, rest)) = line.split_once("Cannot find package ") {
        return quoted(rest);
    }
    if let Some(command) = line
        .strip_suffix(": not found")
        .or_else(|| line.strip_suffix(": command not found"))
    {
        return command.rsplit(": ").next().map(|c| c.trim().to_string());
    }
    None
}

fn managed_workspace(workspace_id: &str) -> Result<PathBuf, String> {
    workspace_under(
        "COMMITARIUM_WORKSPACE_SOURCE",
        ".commitarium/workspaces",
        workspace_id,
    )
}

/// The reviewer's checkout of a work order, which holds its private
/// acceptance-test commit.
fn reviewer_workspace(workspace_id: &str) -> Result<PathBuf, String> {
    workspace_under(
        "COMMITARIUM_REVIEWER_WORKSPACE_SOURCE",
        ".commitarium/reviewer-workspaces",
        workspace_id,
    )
}

fn workspace_under(variable: &str, default: &str, workspace_id: &str) -> Result<PathBuf, String> {
    let source = std::env::var_os(variable)
        .map(PathBuf::from)
        .unwrap_or_else(|| {
            docker::compose_file()
                .unwrap_or_else(|_| PathBuf::from("compose.yml"))
                .parent()
                .unwrap_or(Path::new("."))
                .join(default)
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

    fn failed(output: &str) -> ValidationCommandResult {
        ValidationCommandResult {
            command: "test".into(),
            exit_code: 1,
            output: output.into(),
            duration_ms: 10,
        }
    }

    #[test]
    fn reads_what_is_missing_from_each_toolchain() {
        assert_eq!(
            missing_name("/usr/bin/python: No module named pytest").as_deref(),
            Some("pytest")
        );
        assert_eq!(
            missing_name("ModuleNotFoundError: No module named 'django.contrib'").as_deref(),
            Some("django.contrib")
        );
        assert_eq!(
            missing_name("Error: Cannot find module 'vitest'").as_deref(),
            Some("vitest")
        );
        assert_eq!(
            missing_name("/bin/sh: 1: pytest: not found").as_deref(),
            Some("pytest")
        );
        assert_eq!(
            missing_name("sh: cargo: command not found").as_deref(),
            Some("cargo")
        );
        assert_eq!(missing_name("AssertionError: 2 != 3"), None);
    }

    #[test]
    fn only_an_outside_dependency_missing_from_every_command_is_an_environment_failure() {
        let repository = tempfile::tempdir().unwrap();
        fs::create_dir(repository.path().join("core")).unwrap();
        let pytest = [
            failed("/usr/bin/python: No module named pytest"),
            failed("No module named pytest"),
        ];
        assert_eq!(
            missing_dependency(&pytest, repository.path()).as_deref(),
            Some("pytest")
        );
        // The project's own module missing is the implementation's failure.
        let own = [failed("ModuleNotFoundError: No module named 'core.setup'")];
        assert_eq!(missing_dependency(&own, repository.path()), None);
        let relative = [failed("Error: Cannot find module './setup'")];
        assert_eq!(missing_dependency(&relative, repository.path()), None);
        // One real result, or one ordinary failure, means the tests ran.
        let mixed = [failed("No module named pytest"), failed("AssertionError")];
        assert_eq!(missing_dependency(&mixed, repository.path()), None);
        let mut passing = failed("ok");
        passing.exit_code = 0;
        assert_eq!(
            missing_dependency(
                &[failed("No module named pytest"), passing],
                repository.path()
            ),
            None
        );
        assert_eq!(missing_dependency(&[], repository.path()), None);
    }

    #[test]
    fn commands_run_from_the_repository_root() {
        let root = VALIDATION_SCRIPT.find("cd /workspace").expect("no cd");
        let first = VALIDATION_SCRIPT.find("timeout 30m").expect("no command");
        assert!(root < first);
    }

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
    fn legacy_environment_overlays_yield_their_images() {
        let legacy = "services:\n  codex-worker:\n    build: !reset null\n    image: commitarium-codex-environment:abc\n  codex-reviewer-worker:\n    build: !reset null\n    image: commitarium-codex-environment:abc\n  claude-worker:\n    build: !reset null\n    image: commitarium-claude-environment:def\n";
        assert_eq!(
            overlay_image(legacy, "codex-worker").as_deref(),
            Some("commitarium-codex-environment:abc")
        );
        assert_eq!(
            overlay_image(legacy, "claude-worker").as_deref(),
            Some("commitarium-claude-environment:def")
        );
        assert_eq!(
            overlay_image(
                "services:\n  codex-worker:\n    image: bad;rm\n",
                "codex-worker"
            ),
            None
        );
    }

    fn images(base: &str, built_from: Option<&str>, workers: &[&str]) -> EnvironmentImages {
        EnvironmentImages {
            base: Some(base.into()),
            derivative: Some("sha256:derived".into()),
            built_from: built_from.map(str::to_string),
            workers: workers.iter().map(|image| image.to_string()).collect(),
        }
    }

    #[test]
    fn environments_follow_their_base_images() {
        // No approved environment: the workers run the bases themselves.
        assert_eq!(environment_plan(None), EnvironmentPlan::Current);
        let current = images("sha256:base", Some("sha256:base"), &["sha256:derived"]);
        assert_eq!(environment_plan(Some(&[current])), EnvironmentPlan::Current);
        // The base was rebuilt or pulled since.
        let changed = images("sha256:new", Some("sha256:base"), &["sha256:derived"]);
        assert_eq!(environment_plan(Some(&[changed])), EnvironmentPlan::Rebuild);
        // Built before derivatives recorded their base.
        let unlabelled = images("sha256:base", None, &["sha256:derived"]);
        assert_eq!(
            environment_plan(Some(&[unlabelled])),
            EnvironmentPlan::Rebuild
        );
        // The overlay's image was removed.
        let removed = EnvironmentImages {
            derivative: None,
            ..images("sha256:base", None, &[])
        };
        assert_eq!(environment_plan(Some(&[removed])), EnvironmentPlan::Rebuild);
        // A missing base cannot be built from.
        let no_base = EnvironmentImages {
            base: None,
            ..images("sha256:base", None, &["sha256:derived"])
        };
        assert_eq!(environment_plan(Some(&[no_base])), EnvironmentPlan::Current);
    }

    #[test]
    fn workers_on_an_older_derivative_are_recreated() {
        let rebuilt = images("sha256:base", Some("sha256:base"), &["sha256:old"]);
        let current = images("sha256:base", Some("sha256:base"), &["sha256:derived"]);
        assert_eq!(
            environment_plan(Some(&[current, rebuilt])),
            EnvironmentPlan::Recreate
        );
        // A rebuild comes first; the recreate follows it.
        let changed = images("sha256:new", Some("sha256:base"), &["sha256:old"]);
        assert_eq!(environment_plan(Some(&[changed])), EnvironmentPlan::Rebuild);
    }

    #[test]
    fn image_inspection_reads_the_recorded_base() {
        let labelled = image_info(
            "\"sha256:derived\" {\"commitarium.base-image\":\"sha256:base\",\"other\":\"x\"}\n",
        )
        .unwrap();
        assert_eq!(labelled.id, "sha256:derived");
        assert_eq!(labelled.built_from.as_deref(), Some("sha256:base"));
        let unlabelled = image_info("\"sha256:derived\" {\"other\":\"x\"}").unwrap();
        assert_eq!(unlabelled.built_from, None);
        assert_eq!(image_info("\"sha256:base\" null").unwrap().built_from, None);
        assert!(image_info("garbage").is_none());
    }

    #[test]
    fn the_overlay_names_only_the_built_providers() {
        let both = environment_overlay(&[
            (Provider::Codex, "commitarium-codex-environment:a".into()),
            (Provider::Claude, "commitarium-claude-environment:b".into()),
        ]);
        assert_eq!(
            overlay_image(&both, "codex-agent-template").as_deref(),
            Some("commitarium-codex-environment:a")
        );
        assert_eq!(
            overlay_image(&both, "claude-agent-template").as_deref(),
            Some("commitarium-claude-environment:b")
        );
        let claude_only =
            environment_overlay(&[(Provider::Claude, "commitarium-claude-environment:b".into())]);
        assert_eq!(overlay_image(&claude_only, "codex-agent-template"), None);
        assert!(claude_only.contains("build: !reset null"));
    }

    #[test]
    fn a_provider_left_out_is_rebuilt_once_its_base_is_here() {
        let left_out = EnvironmentImages {
            derivative: None,
            built_from: None,
            ..images("sha256:base", None, &[])
        };
        assert_eq!(
            environment_plan(Some(&[left_out])),
            EnvironmentPlan::Rebuild
        );
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
