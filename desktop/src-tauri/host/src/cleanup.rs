//! Removes the Docker images and volumes Commitarium left behind and no
//! longer uses, so updates and rebuilds do not fill the user's disk.
//!
//! Only Commitarium's own resources are considered, each recognized by its
//! repository or label, never the user's other images or volumes:
//!
//! - release images (`ghcr.io/einarlogioskars/commitarium-*`) of versions
//!   other than the running one, in installed releases only;
//! - approved-environment derivatives (`commitarium-<provider>-environment`)
//!   the environment overlay no longer selects;
//! - untagged images from Commitarium's own builds: the stack, previews and
//!   preview checks (Compose labels their project), and environment
//!   derivatives (labelled with their base image);
//! - the provider and journal volumes of agents that were removed.
//!
//! Nothing a container uses, running or stopped, is removed. The BuildKit
//! cache is left alone: Docker cannot tell Commitarium's build cache from the
//! user's other builds, so it can only be pruned as a whole. Every failure is
//! logged and otherwise ignored.

use std::collections::HashSet;
use std::sync::atomic::{AtomicBool, Ordering};

use crate::{agents, docker, environment};

const RELEASE_REPOSITORIES: &[&str] = &[
    "ghcr.io/einarlogioskars/commitarium-coordinator",
    "ghcr.io/einarlogioskars/commitarium-worker",
    "ghcr.io/einarlogioskars/commitarium-codex-worker",
    "ghcr.io/einarlogioskars/commitarium-claude-worker",
];
const ENVIRONMENT_REPOSITORIES: &[&str] = &[
    "commitarium-codex-environment",
    "commitarium-claude-environment",
];
const COMPOSE_PROJECT_LABEL: &str = "com.docker.compose.project";
const COMPOSE_VOLUME_LABEL: &str = "com.docker.compose.volume";
const NONE: &str = "<none>";

/// One image reference as `docker image ls` lists it.
#[derive(Clone, Debug, Default)]
struct ImageRow {
    id: String,
    repository: String,
    tag: String,
    size: String,
    /// The Compose project label, read only for untagged images.
    project: Option<String>,
    /// Whether the image carries the environment base-image label, read only
    /// for untagged images.
    environment_build: bool,
}

/// One named volume as `docker volume ls` lists it.
#[derive(Clone, Debug, Default)]
struct VolumeRow {
    name: String,
    project: String,
    volume: String,
    /// No container, running or stopped, mounts it.
    dangling: bool,
}

/// What a cleanup would remove, and why.
#[derive(Debug, Eq, PartialEq)]
struct Removal {
    /// The tag to untag, or the ID of an untagged image or a volume's name.
    reference: String,
    size: String,
    reason: &'static str,
}

/// Run a cleanup in the background, at most one at a time. Safe to call
/// whenever Commitarium may have replaced an image.
pub(crate) fn spawn() {
    static RUNNING: AtomicBool = AtomicBool::new(false);
    if RUNNING.swap(true, Ordering::AcqRel) {
        return;
    }
    std::thread::spawn(|| {
        run();
        RUNNING.store(false, Ordering::Release);
    });
}

fn run() {
    // Environment derivatives are tagged before the overlay selects them;
    // waiting for the builds keeps a new one from looking unused.
    let _images = environment::environment_images_lock();
    match plan_images(docker::release_mode().then(release_tag).flatten()) {
        Ok(removals) => {
            for removal in removals {
                remove("image", &removal);
            }
        }
        Err(error) => eprintln!("[cleanup] images: {error}"),
    }
    match plan_volumes() {
        Ok(removals) => {
            for removal in removals {
                remove("volume", &removal);
            }
        }
        Err(error) => eprintln!("[cleanup] volumes: {error}"),
    }
}

fn release_tag() -> Option<String> {
    std::env::var("COMMITARIUM_IMAGE_TAG")
        .ok()
        .filter(|tag| !tag.is_empty())
}

fn remove(kind: &str, removal: &Removal) {
    // Without --force Docker itself still refuses anything a container uses.
    match docker::docker_command()
        .args([kind, "rm", &removal.reference])
        .output()
    {
        Ok(output) if output.status.success() => eprintln!(
            "[cleanup] removed {kind} {} ({}, {})",
            removal.reference, removal.reason, removal.size
        ),
        Ok(output) => eprintln!(
            "[cleanup] could not remove {kind} {}: {}",
            removal.reference,
            String::from_utf8_lossy(&output.stderr).trim()
        ),
        Err(error) => eprintln!(
            "[cleanup] could not remove {kind} {}: {error}",
            removal.reference
        ),
    }
}

fn plan_images(release_tag: Option<String>) -> Result<Vec<Removal>, String> {
    let mut images = parse_images(&docker_output(&[
        "image",
        "ls",
        "--all",
        "--no-trunc",
        "--format",
        "{{.ID}}\t{{.Repository}}\t{{.Tag}}\t{{.Size}}",
    ])?);
    label_untagged(&mut images)?;
    let used = used_images()?;
    let selected = environment::approved_environment_images()?;
    Ok(select_images(
        &images,
        &used,
        release_tag.as_deref(),
        &selected,
    ))
}

/// Read the labels of the untagged images, which alone decide whether an
/// untagged image came from one of Commitarium's builds.
fn label_untagged(images: &mut [ImageRow]) -> Result<(), String> {
    let ids: Vec<String> = images
        .iter()
        .filter(|image| image.repository == NONE && image.tag == NONE)
        .map(|image| image.id.clone())
        .collect::<HashSet<_>>()
        .into_iter()
        .collect();
    if ids.is_empty() {
        return Ok(());
    }
    let mut args = vec!["image", "inspect", "--format", "{{.Id}}\t{{json .Config}}"];
    args.extend(ids.iter().map(String::as_str));
    for line in docker_output(&args)?.lines() {
        let Some((id, config)) = line.split_once('\t') else {
            continue;
        };
        let labels = serde_json::from_str::<serde_json::Value>(config)
            .ok()
            .and_then(|config| config.get("Labels").cloned())
            .unwrap_or_default();
        for image in images.iter_mut().filter(|image| image.id == id) {
            image.project = labels
                .get(COMPOSE_PROJECT_LABEL)
                .and_then(serde_json::Value::as_str)
                .map(str::to_string);
            image.environment_build = labels.get(environment::BASE_IMAGE_LABEL).is_some();
        }
    }
    Ok(())
}

/// The image IDs of every container, running or stopped.
fn used_images() -> Result<HashSet<String>, String> {
    let containers = docker_output(&["ps", "--all", "--quiet", "--no-trunc"])?;
    let containers: Vec<&str> = containers.split_whitespace().collect();
    if containers.is_empty() {
        return Ok(HashSet::new());
    }
    let mut args = vec!["container", "inspect", "--format", "{{.Image}}"];
    args.extend(containers);
    Ok(docker_output(&args)?
        .lines()
        .map(str::trim)
        .filter(|id| !id.is_empty())
        .map(str::to_string)
        .collect())
}

fn plan_volumes() -> Result<Vec<Removal>, String> {
    let volumes = parse_volumes(
        &docker_output(&[
            "volume",
            "ls",
            "--filter",
            &format!("label={COMPOSE_PROJECT_LABEL}={}", docker::PROJECT_NAME),
            "--format",
            &format!(
                "{{{{.Name}}}}\t{{{{.Label \"{COMPOSE_PROJECT_LABEL}\"}}}}\t{{{{.Label \"{COMPOSE_VOLUME_LABEL}\"}}}}"
            ),
        ])?,
        &docker_output(&["volume", "ls", "--quiet", "--filter", "dangling=true"])?,
    );
    // Only the coordinator's own answer counts; a cached or failed list
    // could leave out an agent that still exists.
    let agents: Vec<String> = agents::fetch(false)?
        .into_iter()
        .map(|agent| agent.id)
        .collect();
    Ok(select_volumes(&volumes, &agents))
}

fn docker_output(args: &[&str]) -> Result<String, String> {
    let output = docker::docker_command()
        .args(args)
        .output()
        .map_err(|e| format!("docker {}: {e}", args[..2].join(" ")))?;
    if !output.status.success() {
        return Err(format!(
            "docker {}: {}",
            args[..2].join(" "),
            String::from_utf8_lossy(&output.stderr).trim()
        ));
    }
    Ok(String::from_utf8_lossy(&output.stdout).into_owned())
}

fn parse_images(listing: &str) -> Vec<ImageRow> {
    listing
        .lines()
        .filter_map(|line| {
            let mut fields = line.split('\t');
            let row = ImageRow {
                id: fields.next()?.trim().to_string(),
                repository: fields.next()?.trim().to_string(),
                tag: fields.next()?.trim().to_string(),
                size: fields.next().unwrap_or_default().trim().to_string(),
                ..ImageRow::default()
            };
            (!row.id.is_empty()).then_some(row)
        })
        .collect()
}

fn parse_volumes(listing: &str, dangling: &str) -> Vec<VolumeRow> {
    let dangling: HashSet<&str> = dangling.split_whitespace().collect();
    listing
        .lines()
        .filter_map(|line| {
            let mut fields = line.split('\t');
            let name = fields.next()?.trim().to_string();
            Some(VolumeRow {
                dangling: dangling.contains(name.as_str()),
                project: fields.next()?.trim().to_string(),
                volume: fields.next()?.trim().to_string(),
                name,
            })
        })
        .collect()
}

/// The images to remove: old release tags (only given the running release's
/// tag), environment derivatives the overlay no longer selects, and untagged
/// images of Commitarium's builds. Anything a container uses is kept.
fn select_images(
    images: &[ImageRow],
    used: &HashSet<String>,
    release_tag: Option<&str>,
    selected_environments: &[String],
) -> Vec<Removal> {
    let mut removals = Vec::new();
    let mut seen = HashSet::new();
    for image in images {
        if used.contains(&image.id) {
            continue;
        }
        let tagged = image.repository != NONE && image.tag != NONE;
        let reference = format!("{}:{}", image.repository, image.tag);
        let (reference, reason) = if tagged
            && release_tag.is_some_and(|current| image.tag != current)
            && RELEASE_REPOSITORIES.contains(&image.repository.as_str())
        {
            (reference, "an earlier release")
        } else if tagged
            && ENVIRONMENT_REPOSITORIES.contains(&image.repository.as_str())
            && !selected_environments.contains(&reference)
        {
            (reference, "a replaced agent environment")
        } else if !tagged
            && (image.environment_build
                || image.project.as_deref().is_some_and(|project| {
                    project == docker::PROJECT_NAME
                        || project.starts_with(crate::preview::PREVIEW_TEMP_PREFIX)
                }))
        {
            (image.id.clone(), "an untagged Commitarium build")
        } else {
            continue;
        };
        if seen.insert(reference.clone()) {
            removals.push(Removal {
                reference,
                size: image.size.clone(),
                reason,
            });
        }
    }
    removals
}

/// The volumes of removed agents that no container mounts. Without any
/// agent listed nothing is removed, so a coordinator that has lost its
/// agents never costs the user their logins.
fn select_volumes(volumes: &[VolumeRow], agents: &[String]) -> Vec<Removal> {
    if agents.is_empty() {
        return Vec::new();
    }
    volumes
        .iter()
        .filter(|volume| {
            volume.dangling
                && volume.project == docker::PROJECT_NAME
                && volume.name == format!("{}_{}", docker::PROJECT_NAME, volume.volume)
                && removed_agent_volume(&volume.volume, agents)
        })
        .map(|volume| Removal {
            reference: volume.name.clone(),
            size: String::new(),
            reason: "a removed agent",
        })
        .collect()
}

/// Whether `volume` is the provider or journal volume of an agent that is
/// not in `agents`.
fn removed_agent_volume(volume: &str, agents: &[String]) -> bool {
    let Some(id) = volume.strip_prefix("agent-").and_then(|rest| {
        rest.strip_suffix("-profile")
            .or_else(|| rest.strip_suffix("-journal"))
    }) else {
        return false;
    };
    agents::valid_id(id) && !agents.iter().any(|agent| agent == id) && {
        let (profile, journal) = agents::volumes(id);
        volume == profile || volume == journal
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tagged(id: &str, reference: &str) -> ImageRow {
        let (repository, tag) = reference.rsplit_once(':').unwrap();
        ImageRow {
            id: id.into(),
            repository: repository.into(),
            tag: tag.into(),
            size: "1GB".into(),
            ..ImageRow::default()
        }
    }

    fn untagged(id: &str, project: Option<&str>, environment_build: bool) -> ImageRow {
        ImageRow {
            id: id.into(),
            repository: NONE.into(),
            tag: NONE.into(),
            size: "1GB".into(),
            project: project.map(str::to_string),
            environment_build,
        }
    }

    fn references(removals: &[Removal]) -> Vec<&str> {
        removals
            .iter()
            .map(|removal| removal.reference.as_str())
            .collect()
    }

    #[test]
    fn earlier_releases_go_and_the_running_one_stays() {
        let images = [
            tagged("a", "ghcr.io/einarlogioskars/commitarium-coordinator:0.7.0"),
            tagged("b", "ghcr.io/einarlogioskars/commitarium-coordinator:0.8.0"),
            tagged(
                "c",
                "ghcr.io/einarlogioskars/commitarium-claude-worker:0.7.0",
            ),
            tagged(
                "d",
                "ghcr.io/einarlogioskars/commitarium-codex-worker:0.6.0",
            ),
            // Still used by a stopped container.
            tagged("e", "ghcr.io/einarlogioskars/commitarium-worker:0.7.0"),
            // Not Commitarium's.
            tagged("f", "ghcr.io/someone/commitarium-worker:0.7.0"),
            tagged("g", "postgres:16-alpine"),
        ];
        let used = HashSet::from(["e".to_string()]);
        assert_eq!(
            references(&select_images(&images, &used, Some("0.8.0"), &[])),
            [
                "ghcr.io/einarlogioskars/commitarium-coordinator:0.7.0",
                "ghcr.io/einarlogioskars/commitarium-claude-worker:0.7.0",
                "ghcr.io/einarlogioskars/commitarium-codex-worker:0.6.0",
            ]
        );
    }

    #[test]
    fn development_keeps_every_release_and_its_own_builds() {
        let images = [
            tagged("a", "ghcr.io/einarlogioskars/commitarium-coordinator:0.7.0"),
            tagged("b", "commitarium-coordinator:latest"),
            tagged("c", "commitarium-codex-agent-worker:latest"),
            tagged("d", "commitarium-claude-worker:latest"),
            tagged("e", "commitarium-preview-prj_1-web:latest"),
        ];
        assert!(select_images(&images, &HashSet::new(), None, &[]).is_empty());
    }

    #[test]
    fn only_the_selected_and_running_environments_stay() {
        let images = [
            tagged("a", "commitarium-codex-environment:new"),
            tagged("b", "commitarium-codex-environment:old"),
            tagged("c", "commitarium-claude-environment:new"),
            tagged("d", "commitarium-claude-environment:old"),
            tagged("e", "commitarium-claude-environment:running"),
            tagged("f", "someone-codex-environment:old"),
        ];
        let used = HashSet::from(["e".to_string()]);
        let selected = [
            "commitarium-codex-environment:new".to_string(),
            "commitarium-claude-environment:new".to_string(),
        ];
        assert_eq!(
            references(&select_images(&images, &used, None, &selected)),
            [
                "commitarium-codex-environment:old",
                "commitarium-claude-environment:old"
            ]
        );
        // Without an approved environment every unused derivative goes.
        assert_eq!(
            select_images(&images, &used, None, &[]).len(),
            4,
            "the running derivative stays"
        );
    }

    #[test]
    fn untagged_images_go_only_when_commitarium_built_them() {
        let images = [
            untagged("stack", Some("commitarium"), false),
            untagged("preview", Some("commitarium-preview-prj_1"), false),
            untagged("check", Some("commitarium-preview-prj_1-check"), false),
            untagged("environment", None, true),
            untagged("other-project", Some("odyno"), false),
            untagged("lookalike", Some("commitarium-other"), false),
            untagged("unlabelled", None, false),
            untagged("running", Some("commitarium"), false),
            // Listed once per platform or reference: removed once.
            untagged("stack", Some("commitarium"), false),
        ];
        let used = HashSet::from(["running".to_string()]);
        assert_eq!(
            references(&select_images(&images, &used, Some("0.8.0"), &[])),
            ["stack", "preview", "check", "environment"]
        );
    }

    #[test]
    fn image_listings_are_parsed_with_their_sizes() {
        let rows = parse_images(
            "sha256:aa\tghcr.io/einarlogioskars/commitarium-worker\t0.7.0\t61.9MB\nsha256:bb\t<none>\t<none>\t1GB\n\n",
        );
        assert_eq!(rows.len(), 2);
        assert_eq!(rows[0].tag, "0.7.0");
        assert_eq!(rows[0].size, "61.9MB");
        assert_eq!(rows[1].repository, NONE);
    }

    fn volume_line(name: &str) -> String {
        let (project, volume) = name.split_once('_').unwrap();
        format!("{name}\t{project}\t{volume}\n")
    }

    #[test]
    fn only_unmounted_volumes_of_removed_agents_go() {
        let listing: String = [
            "commitarium_agent-gone-profile",
            "commitarium_agent-gone-journal",
            "commitarium_agent-kept-profile",
            "commitarium_agent-kept-journal",
            "commitarium_agent-mounted-profile",
            "commitarium_codex-profile",
            "commitarium_codex-reviewer-profile",
            "commitarium_claude-worker-journal",
            "commitarium_coordinator-data",
            "commitarium_forgejo-data",
            "commitarium_commitarium-credentials",
            "commitarium_commitarium-toolchains",
            "commitarium_codex-cli",
            "commitarium_agent-codex-profile",
            "commitarium_agent--profile",
        ]
        .iter()
        .map(|name| volume_line(name))
        .collect();
        let dangling = listing
            .lines()
            .filter_map(|line| line.split('\t').next())
            .filter(|name| !name.is_empty() && *name != "commitarium_agent-mounted-profile")
            .collect::<Vec<_>>()
            .join("\n");
        let volumes = parse_volumes(&listing, &dangling);
        let agents = ["kept".to_string(), "codex".to_string()];
        assert_eq!(
            references(&select_volumes(&volumes, &agents)),
            [
                "commitarium_agent-gone-profile",
                "commitarium_agent-gone-journal"
            ]
        );
    }

    #[test]
    fn volumes_of_other_projects_and_without_agents_stay() {
        let volumes = parse_volumes(
            "odyno_agent-gone-profile\todyno\tagent-gone-profile\ncommitarium-preview-prj_1.data\tcommitarium-preview-prj_1\tdata\nother_name\tcommitarium\tagent-gone-journal\n",
            "odyno_agent-gone-profile\ncommitarium-preview-prj_1.data\nother_name\n",
        );
        assert!(select_volumes(&volumes, &["kept".to_string()]).is_empty());
        let gone = parse_volumes(
            "commitarium_agent-gone-profile\tcommitarium\tagent-gone-profile\n",
            "commitarium_agent-gone-profile\n",
        );
        assert!(select_volumes(&gone, &[]).is_empty());
        assert_eq!(select_volumes(&gone, &["kept".to_string()]).len(), 1);
    }

    /// Lists what a cleanup would remove from this machine's Docker, as an
    /// installed release of this version would, without removing anything.
    #[test]
    #[ignore = "reads the local Docker and coordinator"]
    fn live_dry_run() {
        if std::env::var_os("COMMITARIUM_ENVIRONMENT_COMPOSE_FILE").is_none() {
            let home = std::env::var("HOME").unwrap();
            std::env::set_var(
                "COMMITARIUM_ENVIRONMENT_COMPOSE_FILE",
                format!("{home}/Library/Application Support/com.einarlogi.commitarium/runtime/compose.environment.yml"),
            );
        }
        let release = env!("CARGO_PKG_VERSION").to_string();
        for removal in plan_images(Some(release)).unwrap() {
            println!(
                "image\t{}\t{}\t{}",
                removal.reference, removal.size, removal.reason
            );
        }
        for removal in plan_volumes().unwrap() {
            println!("volume\t{}\t{}", removal.reference, removal.reason);
        }
    }
}
