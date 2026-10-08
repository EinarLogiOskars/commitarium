//! Validation and rewriting for untrusted project Compose files.
//!
//! Docker Compose performs interpolation and normalization first. We then
//! validate the normalized JSON against the ADR-014 allowlist and write a new
//! model owned by Commitarium. Preview lifecycle code only ever launches that
//! rewritten model.

use crate::docker;
use serde_json::{json, Map, Value};
use std::fs;
use std::path::{Path, PathBuf};

const COMPOSE_FILENAMES: &[&str] = &[
    "compose.yaml",
    "compose.yml",
    "docker-compose.yaml",
    "docker-compose.yml",
];
const SERVICE_KEYS: &[&str] = &[
    "image",
    "build",
    "command",
    "entrypoint",
    "environment",
    "working_dir",
    "user",
    "ports",
    "expose",
    "volumes",
    "depends_on",
    "healthcheck",
    "restart",
    "init",
    "tty",
    "stdin_open",
    "labels",
    "networks",
];
const BUILD_KEYS: &[&str] = &["context", "dockerfile", "args", "target"];
const MOUNT_KEYS: &[&str] = &[
    "type",
    "source",
    "target",
    "read_only",
    "consistency",
    "bind",
    "volume",
    "tmpfs",
];

#[derive(Clone, Debug, Eq, PartialEq)]
pub(crate) struct OpenTarget {
    pub(crate) service: String,
    pub(crate) port: u16,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub(crate) struct PublishedPort {
    pub(crate) service: String,
    pub(crate) target: u16,
    pub(crate) protocol: String,
}

#[derive(Clone, Debug)]
pub(crate) struct PreparedCompose {
    pub(crate) project_name: String,
    pub(crate) model: Value,
    pub(crate) published_ports: Vec<PublishedPort>,
}

pub(crate) fn project_name(project_id: &str) -> Result<String, String> {
    if project_id.is_empty()
        || project_id.len() > 80
        || !project_id
            .bytes()
            .all(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit() || b"_-".contains(&byte))
        || !project_id.as_bytes()[0].is_ascii_alphanumeric()
    {
        return Err("project ID cannot be used as a Compose project name".into());
    }
    Ok(format!("commitarium-preview-{project_id}"))
}

pub(crate) fn find_compose_file(checkout: &Path) -> Result<PathBuf, String> {
    for name in COMPOSE_FILENAMES {
        let candidate = checkout.join(name);
        if candidate.is_file() {
            return Ok(candidate);
        }
    }
    Err(
        "add a compose.yaml or compose.yml file at the repository root before starting a preview"
            .into(),
    )
}

pub(crate) fn normalize_and_rewrite(
    checkout: &Path,
    project_id: &str,
    open: &OpenTarget,
) -> Result<PreparedCompose, String> {
    let compose_file = find_compose_file(checkout)?;
    let output = docker::docker_command()
        .args(["compose", "-f"])
        .arg(&compose_file)
        .args(["config", "--format", "json"])
        .current_dir(checkout)
        .output()
        .map_err(|e| format!("validate project Compose file: {e}"))?;
    if !output.status.success() {
        return Err(format!(
            "the project Compose file is invalid: {}",
            bounded(&String::from_utf8_lossy(&output.stderr))
        ));
    }
    let model: Value = serde_json::from_slice(&output.stdout)
        .map_err(|e| format!("decode normalized project Compose file: {e}"))?;
    validate_and_rewrite(checkout, project_id, open, model)
}

pub(crate) fn validate_and_rewrite(
    checkout: &Path,
    project_id: &str,
    open: &OpenTarget,
    mut model: Value,
) -> Result<PreparedCompose, String> {
    let checkout = checkout
        .canonicalize()
        .map_err(|e| format!("resolve preview checkout: {e}"))?;
    let compose_project = project_name(project_id)?;
    let root = object_mut(&mut model, "Compose document")?;
    validate_keys(
        root,
        &["name", "services", "volumes", "networks"],
        "Compose document",
    )?;

    if let Some(volumes) = root.get("volumes") {
        validate_top_volumes(volumes)?;
    }
    if let Some(networks) = root.get("networks") {
        validate_top_networks(networks)?;
    }

    let services = root
        .get_mut("services")
        .ok_or_else(|| "Compose document has no services".to_string())?
        .as_object_mut()
        .ok_or_else(|| "Compose services must be an object".to_string())?;
    if services.is_empty() {
        return Err("Compose document has no services".into());
    }
    if !services.contains_key(&open.service) {
        return Err(format!(
            "preview open service {:?} is not defined by the Compose file",
            open.service
        ));
    }

    let mut published = Vec::new();
    for (service_name, service_value) in services.iter_mut() {
        let service = object_mut(service_value, &format!("service {service_name:?}"))?;
        validate_keys(service, SERVICE_KEYS, &format!("service {service_name:?}"))?;
        if let Some(build) = service.get("build") {
            validate_build(build, &checkout, service_name)?;
        }
        if let Some(volumes) = service.get("volumes") {
            validate_mounts(volumes, &checkout, service_name)?;
        }
        if let Some(ports) = service.get_mut("ports") {
            rewrite_ports(ports, service_name, &mut published)?;
        }
        rewrite_labels(service, &compose_project)?;
        service.insert("security_opt".into(), json!(["no-new-privileges:true"]));
        service.insert("cpus".into(), json!(2.0));
        service.insert("mem_limit".into(), json!("4g"));
        service.insert("pids_limit".into(), json!(512));
    }

    if !published.iter().any(|port| {
        port.service == open.service && port.target == open.port && port.protocol == "tcp"
    }) {
        return Err(format!(
            "preview open target {}:{} must be published as a TCP port by the Compose file",
            open.service, open.port
        ));
    }
    root.insert("name".into(), Value::String(compose_project.clone()));
    Ok(PreparedCompose {
        project_name: compose_project,
        model,
        published_ports: published,
    })
}

pub(crate) fn write_rewritten(prepared: &PreparedCompose, path: &Path) -> Result<(), String> {
    let encoded = serde_json::to_vec_pretty(&prepared.model)
        .map_err(|e| format!("encode rewritten preview Compose file: {e}"))?;
    fs::write(path, encoded).map_err(|e| format!("write rewritten preview Compose file: {e}"))
}

fn validate_build(build: &Value, checkout: &Path, service: &str) -> Result<(), String> {
    let (context, dockerfile) = match build {
        Value::String(context) => (context.as_str(), None),
        Value::Object(build) => {
            validate_keys(build, BUILD_KEYS, &format!("service {service:?} build"))?;
            let context = build
                .get("context")
                .and_then(Value::as_str)
                .ok_or_else(|| format!("service {service:?} build context is missing"))?;
            (context, build.get("dockerfile").and_then(Value::as_str))
        }
        _ => {
            return Err(format!(
                "service {service:?} build must be a string or object"
            ))
        }
    };
    let context = resolve_inside(checkout, Path::new(context), "build context")?;
    if let Some(dockerfile) = dockerfile {
        resolve_inside(&context, Path::new(dockerfile), "Dockerfile")?;
    }
    Ok(())
}

fn validate_mounts(value: &Value, checkout: &Path, service: &str) -> Result<(), String> {
    let mounts = value
        .as_array()
        .ok_or_else(|| format!("service {service:?} volumes must be an array"))?;
    for mount in mounts {
        match mount {
            Value::String(spec) => validate_short_mount(spec, checkout)?,
            Value::Object(mount) => {
                validate_keys(
                    mount,
                    MOUNT_KEYS,
                    &format!("service {service:?} volume mount"),
                )?;
                let kind = mount
                    .get("type")
                    .and_then(Value::as_str)
                    .unwrap_or("volume");
                match kind {
                    "bind" => {
                        let source =
                            mount.get("source").and_then(Value::as_str).ok_or_else(|| {
                                format!("service {service:?} bind mount has no source")
                            })?;
                        resolve_inside(checkout, Path::new(source), "bind mount source")?;
                        if let Some(bind) = mount.get("bind") {
                            let bind = bind.as_object().ok_or_else(|| {
                                format!("service {service:?} bind options must be an object")
                            })?;
                            validate_keys(bind, &["create_host_path"], "bind mount options")?;
                        }
                    }
                    "volume" => {
                        if let Some(options) = mount.get("volume") {
                            let options = options.as_object().ok_or_else(|| {
                                format!("service {service:?} volume options must be an object")
                            })?;
                            validate_keys(options, &["nocopy", "subpath"], "volume mount options")?;
                        }
                    }
                    "tmpfs" => {
                        if mount.get("source").is_some() {
                            return Err(format!(
                                "service {service:?} tmpfs mount cannot have a source"
                            ));
                        }
                        if let Some(options) = mount.get("tmpfs") {
                            let options = options.as_object().ok_or_else(|| {
                                format!("service {service:?} tmpfs options must be an object")
                            })?;
                            validate_keys(options, &["size", "mode"], "tmpfs mount options")?;
                        }
                    }
                    _ => {
                        return Err(format!(
                            "service {service:?} uses unsupported mount type {kind:?}"
                        ))
                    }
                }
            }
            _ => return Err(format!("service {service:?} has an invalid volume mount")),
        }
    }
    Ok(())
}

fn validate_short_mount(spec: &str, checkout: &Path) -> Result<(), String> {
    let source = spec.split(':').next().unwrap_or_default();
    if source.starts_with('.') || source.starts_with('/') {
        resolve_inside(checkout, Path::new(source), "bind mount source")?;
    }
    Ok(())
}

fn validate_top_volumes(value: &Value) -> Result<(), String> {
    let volumes = value
        .as_object()
        .ok_or_else(|| "top-level volumes must be an object".to_string())?;
    for (name, volume) in volumes {
        let volume = volume
            .as_object()
            .ok_or_else(|| format!("volume {name:?} must be an object"))?;
        validate_keys(
            volume,
            &["name", "driver", "labels"],
            &format!("volume {name:?}"),
        )?;
        if volume
            .get("driver")
            .and_then(Value::as_str)
            .is_some_and(|driver| driver != "local")
        {
            return Err(format!("volume {name:?} must use the local driver"));
        }
    }
    Ok(())
}

fn validate_top_networks(value: &Value) -> Result<(), String> {
    let networks = value
        .as_object()
        .ok_or_else(|| "top-level networks must be an object".to_string())?;
    for (name, network) in networks {
        let network = network
            .as_object()
            .ok_or_else(|| format!("network {name:?} must be an object"))?;
        validate_keys(
            network,
            &[
                "name",
                "driver",
                "labels",
                "attachable",
                "enable_ipv4",
                "enable_ipv6",
                "ipam",
            ],
            &format!("network {name:?}"),
        )?;
        if network
            .get("driver")
            .and_then(Value::as_str)
            .is_some_and(|driver| driver != "bridge" && driver != "default")
        {
            return Err(format!("network {name:?} must use the bridge driver"));
        }
    }
    Ok(())
}

fn rewrite_ports(
    value: &mut Value,
    service: &str,
    published: &mut Vec<PublishedPort>,
) -> Result<(), String> {
    let ports = value
        .as_array_mut()
        .ok_or_else(|| format!("service {service:?} ports must be an array"))?;
    for port in ports.iter_mut() {
        let (target, protocol) = parse_port(port, service)?;
        *port = json!({
            "target": target,
            "published": "0",
            "host_ip": "127.0.0.1",
            "protocol": protocol,
            "mode": "ingress"
        });
        published.push(PublishedPort {
            service: service.to_string(),
            target,
            protocol,
        });
    }
    Ok(())
}

fn parse_port(value: &Value, service: &str) -> Result<(u16, String), String> {
    match value {
        Value::Number(number) => number
            .as_u64()
            .and_then(|port| u16::try_from(port).ok())
            .filter(|port| *port != 0)
            .map(|port| (port, "tcp".to_string())),
        Value::String(spec) => {
            let (spec, protocol) = spec.rsplit_once('/').unwrap_or((spec, "tcp"));
            if spec.contains('-') {
                return Err(format!("service {service:?} port ranges are not supported"));
            }
            spec.rsplit(':')
                .next()
                .and_then(|port| port.parse::<u16>().ok())
                .filter(|port| *port != 0)
                .map(|port| (port, protocol.to_string()))
        }
        Value::Object(port) => {
            validate_keys(
                port,
                &[
                    "target",
                    "published",
                    "host_ip",
                    "protocol",
                    "mode",
                    "name",
                    "app_protocol",
                ],
                &format!("service {service:?} published port"),
            )?;
            let target = port
                .get("target")
                .and_then(value_u16)
                .filter(|port| *port != 0)
                .ok_or_else(|| format!("service {service:?} has an invalid published port"))?;
            let protocol = port
                .get("protocol")
                .and_then(Value::as_str)
                .unwrap_or("tcp")
                .to_string();
            return Ok((target, protocol));
        }
        _ => None,
    }
    .ok_or_else(|| format!("service {service:?} has an invalid published port"))
}

fn rewrite_labels(service: &mut Map<String, Value>, project: &str) -> Result<(), String> {
    let labels = service.entry("labels").or_insert_with(|| json!({}));
    if labels.is_array() {
        let mut normalized = Map::new();
        for label in labels.as_array().into_iter().flatten() {
            let label = label
                .as_str()
                .ok_or_else(|| "service labels must contain strings".to_string())?;
            let (key, value) = label.split_once('=').unwrap_or((label, ""));
            normalized.insert(key.to_string(), Value::String(value.to_string()));
        }
        *labels = Value::Object(normalized);
    }
    let labels = labels
        .as_object_mut()
        .ok_or_else(|| "service labels must be an object or array".to_string())?;
    labels.insert("commitarium.preview".into(), Value::String("true".into()));
    labels.insert(
        "commitarium.preview.project".into(),
        Value::String(project.to_string()),
    );
    Ok(())
}

fn resolve_inside(base: &Path, configured: &Path, kind: &str) -> Result<PathBuf, String> {
    let path = if configured.is_absolute() {
        configured.to_path_buf()
    } else {
        base.join(configured)
    };
    let resolved = path
        .canonicalize()
        .map_err(|e| format!("resolve {kind} {}: {e}", path.display()))?;
    if !resolved.starts_with(base) {
        return Err(format!(
            "{kind} {} resolves outside the repository",
            path.display()
        ));
    }
    Ok(resolved)
}

fn validate_keys(object: &Map<String, Value>, allowed: &[&str], name: &str) -> Result<(), String> {
    for key in object.keys() {
        if !allowed.contains(&key.as_str()) && !key.starts_with("x-") {
            return Err(format!("{name} uses unsupported field {key:?}"));
        }
    }
    Ok(())
}

fn object_mut<'a>(value: &'a mut Value, name: &str) -> Result<&'a mut Map<String, Value>, String> {
    value
        .as_object_mut()
        .ok_or_else(|| format!("{name} must be an object"))
}

fn value_u16(value: &Value) -> Option<u16> {
    value
        .as_u64()
        .and_then(|value| u16::try_from(value).ok())
        .or_else(|| value.as_str().and_then(|value| value.parse().ok()))
}

fn bounded(value: &str) -> String {
    value.chars().take(4000).collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn full_stack(root: &Path) -> Value {
        fs::create_dir_all(root.join("web")).unwrap();
        fs::create_dir_all(root.join("api")).unwrap();
        fs::write(root.join("web/Dockerfile"), "FROM scratch\n").unwrap();
        fs::write(root.join("api/Dockerfile"), "FROM scratch\n").unwrap();
        json!({
            "name": "untrusted",
            "services": {
                "web": {
                    "build": {"context": root.join("web"), "dockerfile": "Dockerfile", "args": {}, "target": "dev"},
                    "ports": [{"target": 5173, "published": "5173", "host_ip": "0.0.0.0", "protocol": "tcp"}],
                    "volumes": [{"type": "bind", "source": root.join("web"), "target": "/app", "bind": {"create_host_path": true}}],
                    "depends_on": {"api": {"condition": "service_started", "required": true}},
                    "networks": ["default"]
                },
                "api": {
                    "build": {"context": root.join("api"), "dockerfile": "Dockerfile"},
                    "ports": [{"target": 8000, "published": "8000", "protocol": "tcp"}],
                    "environment": {"DATABASE_URL": "postgres://postgres@db/app"},
                    "depends_on": ["db"]
                },
                "db": {
                    "image": "postgres:18",
                    "volumes": [{"type": "volume", "source": "data", "target": "/var/lib/postgresql/data"}]
                }
            },
            "volumes": {"data": {"name": "untrusted_data", "driver": "local"}},
            "networks": {"default": {"name": "untrusted_default", "driver": "bridge"}}
        })
    }

    #[test]
    fn accepts_and_rewrites_a_full_stack() {
        let root = tempfile::tempdir().unwrap();
        let prepared = validate_and_rewrite(
            root.path(),
            "prj_test",
            &OpenTarget {
                service: "web".into(),
                port: 5173,
            },
            full_stack(root.path()),
        )
        .unwrap();
        assert_eq!(prepared.project_name, "commitarium-preview-prj_test");
        assert_eq!(prepared.published_ports.len(), 2);
        let web = &prepared.model["services"]["web"];
        assert_eq!(web["ports"][0]["host_ip"], "127.0.0.1");
        assert_eq!(web["ports"][0]["published"], "0");
        assert_eq!(web["security_opt"][0], "no-new-privileges:true");
        assert_eq!(web["labels"]["commitarium.preview"], "true");
        assert_eq!(web["pids_limit"], 512);
    }

    #[test]
    fn rejects_disallowed_service_fields() {
        for field in [
            "privileged",
            "cap_add",
            "devices",
            "network_mode",
            "pid",
            "ipc",
            "userns_mode",
            "security_opt",
            "volumes_from",
            "secrets",
            "configs",
        ] {
            let root = tempfile::tempdir().unwrap();
            let mut model = full_stack(root.path());
            model["services"]["web"][field] = json!(true);
            let error = validate_and_rewrite(
                root.path(),
                "prj_test",
                &OpenTarget {
                    service: "web".into(),
                    port: 5173,
                },
                model,
            )
            .unwrap_err();
            assert!(error.contains(field), "error for {field}: {error}");
        }
    }

    #[test]
    fn rejects_unsafe_top_level_resources_and_build_features() {
        for (parent, field, value) in [
            ("/volumes/data", "driver_opts", json!({"type": "none"})),
            ("/volumes/data", "external", json!(true)),
            ("/networks/default", "external", json!(true)),
            ("/services/web/build", "secrets", json!(["token"])),
            ("/services/web/build", "ssh", json!(["default"])),
            ("/services/web/build", "network", json!("host")),
        ] {
            let root = tempfile::tempdir().unwrap();
            let mut model = full_stack(root.path());
            model
                .pointer_mut(parent)
                .unwrap_or_else(|| panic!("missing {parent}"))
                .as_object_mut()
                .unwrap()
                .insert(field.into(), value);
            let error = validate_and_rewrite(
                root.path(),
                "prj_test",
                &OpenTarget {
                    service: "web".into(),
                    port: 5173,
                },
                model,
            )
            .unwrap_err();
            assert!(error.contains(field), "error for {parent}/{field}: {error}");
        }
    }

    #[test]
    fn rejects_parent_and_outside_build_paths() {
        let parent = tempfile::tempdir().unwrap();
        let root = parent.path().join("repo");
        fs::create_dir(&root).unwrap();
        fs::create_dir(parent.path().join("outside")).unwrap();
        let mut model = full_stack(&root);
        model["services"]["web"]["volumes"][0]["source"] = json!("../outside");
        assert!(validate_and_rewrite(
            &root,
            "prj_test",
            &OpenTarget {
                service: "web".into(),
                port: 5173
            },
            model,
        )
        .unwrap_err()
        .contains("outside the repository"));

        let mut model = full_stack(&root);
        model["services"]["web"]["build"]["context"] = json!(parent.path().join("outside"));
        assert!(validate_and_rewrite(
            &root,
            "prj_test",
            &OpenTarget {
                service: "web".into(),
                port: 5173
            },
            model,
        )
        .unwrap_err()
        .contains("outside the repository"));
    }

    #[cfg(unix)]
    #[test]
    fn rejects_symlink_escape() {
        use std::os::unix::fs::symlink;
        let parent = tempfile::tempdir().unwrap();
        let root = parent.path().join("repo");
        fs::create_dir(&root).unwrap();
        fs::create_dir(parent.path().join("outside")).unwrap();
        symlink(parent.path().join("outside"), root.join("escape")).unwrap();
        let mut model = full_stack(&root);
        model["services"]["web"]["volumes"][0]["source"] = json!(root.join("escape"));
        assert!(validate_and_rewrite(
            &root,
            "prj_test",
            &OpenTarget {
                service: "web".into(),
                port: 5173
            },
            model,
        )
        .unwrap_err()
        .contains("outside the repository"));
    }
}
