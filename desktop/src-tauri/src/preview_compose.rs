//! Validation and rewriting for untrusted project Compose files.
//!
//! Docker Compose performs interpolation and normalization first. We then
//! validate the normalized JSON against the ADR-014 allowlist and write a new
//! model owned by Commitarium. Preview lifecycle code only ever launches that
//! rewritten model.

use crate::docker;
use serde_json::{json, Map, Value};
use std::ffi::OsString;
use std::fs;
use std::path::{Path, PathBuf};
use std::process::Command;

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
const MAX_COMPOSE_BYTES: u64 = 1024 * 1024;
const MAX_YAML_DEPTH: usize = 64;
const SAFE_DOCKER_ENV: &[&str] = &[
    "DOCKER_API_VERSION",
    "DOCKER_CERT_PATH",
    "DOCKER_CONFIG",
    "DOCKER_CONTEXT",
    "DOCKER_HOST",
    "DOCKER_TLS_VERIFY",
];
const RAW_FILE_KEYS: &[&str] = &[
    "configs",
    "env_file",
    "extends",
    "include",
    "label_file",
    "secrets",
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
    let checkout = checkout
        .canonicalize()
        .map_err(|e| format!("resolve preview checkout: {e}"))?;
    for name in COMPOSE_FILENAMES {
        let candidate = checkout.join(name);
        match fs::symlink_metadata(&candidate) {
            Ok(metadata) if metadata.file_type().is_symlink() => {
                return Err("the root Compose file cannot be a symbolic link".into());
            }
            Ok(metadata) if metadata.is_file() => {
                let resolved = candidate
                    .canonicalize()
                    .map_err(|e| format!("resolve project Compose file: {e}"))?;
                if !resolved.starts_with(&checkout) {
                    return Err("the root Compose file resolves outside the repository".into());
                }
                return Ok(resolved);
            }
            Ok(_) | Err(_) => {}
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
    validate_raw_compose(&compose_file)?;
    let empty_environment = tempfile::NamedTempFile::new()
        .map_err(|e| format!("create isolated Compose environment file: {e}"))?;
    let mut command = docker::docker_command();
    sanitize_compose_command(&mut command, checkout);
    let output = command
        .args(["compose", "--env-file"])
        .arg(empty_environment.path())
        .arg("-f")
        .arg(&compose_file)
        .args(["config", "--no-interpolate", "--format", "json"])
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
    reject_normalized_interpolation(&model)?;
    let root = object_mut(&mut model, "Compose document")?;
    validate_keys_with_extensions(
        root,
        &["name", "services", "volumes", "networks"],
        "Compose document",
    )?;

    if let Some(volumes) = root.get_mut("volumes") {
        validate_top_volumes(volumes, &compose_project)?;
    }
    if let Some(networks) = root.get_mut("networks") {
        validate_top_networks(networks, &compose_project)?;
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
        validate_keys_with_extensions(service, SERVICE_KEYS, &format!("service {service_name:?}"))?;
        if service.contains_key("build") && service.contains_key("image") {
            return Err(format!(
                "service {service_name:?} cannot set image when build is present"
            ));
        }
        if let Some(build) = service.get("build") {
            validate_build(build, &checkout, service_name)?;
        }
        if let Some(environment) = service.get("environment") {
            validate_environment(environment, service_name)?;
        }
        if let Some(volumes) = service.get("volumes") {
            validate_mounts(volumes, &checkout, service_name)?;
        }
        if let Some(networks) = service.get("networks") {
            validate_service_networks(networks, service_name)?;
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

fn validate_environment(value: &Value, service: &str) -> Result<(), String> {
    match value {
        Value::Object(environment) => {
            for (name, value) in environment {
                if value.is_null() {
                    return Err(format!(
                        "service {service:?} environment variable {name:?} must have an explicit value"
                    ));
                }
            }
            Ok(())
        }
        Value::Array(environment) => {
            for entry in environment {
                let entry = entry.as_str().ok_or_else(|| {
                    format!("service {service:?} environment entries must be strings")
                })?;
                if !entry.contains('=') {
                    return Err(format!(
                        "service {service:?} environment entry {entry:?} must have an explicit value"
                    ));
                }
            }
            Ok(())
        }
        _ => Err(format!(
            "service {service:?} environment must be an object or array"
        )),
    }
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

fn validate_top_volumes(value: &mut Value, project: &str) -> Result<(), String> {
    let volumes = value
        .as_object_mut()
        .ok_or_else(|| "top-level volumes must be an object".to_string())?;
    for (name, volume) in volumes.iter_mut() {
        let volume = volume
            .as_object_mut()
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
        volume.remove("labels");
        volume.insert(
            "name".into(),
            Value::String(owned_resource_name(project, name)),
        );
    }
    Ok(())
}

fn validate_top_networks(value: &mut Value, project: &str) -> Result<(), String> {
    let networks = value
        .as_object_mut()
        .ok_or_else(|| "top-level networks must be an object".to_string())?;
    for (name, network) in networks.iter_mut() {
        let network = network
            .as_object_mut()
            .ok_or_else(|| format!("network {name:?} must be an object"))?;
        validate_keys(
            network,
            &["name", "driver", "labels", "enable_ipv4", "enable_ipv6"],
            &format!("network {name:?}"),
        )?;
        if network
            .get("driver")
            .and_then(Value::as_str)
            .is_some_and(|driver| driver != "bridge" && driver != "default")
        {
            return Err(format!("network {name:?} must use the bridge driver"));
        }
        network.remove("labels");
        network.insert(
            "name".into(),
            Value::String(owned_resource_name(project, name)),
        );
    }
    Ok(())
}

fn validate_service_networks(value: &Value, service: &str) -> Result<(), String> {
    match value {
        Value::Array(networks) => {
            if networks.iter().all(Value::is_string) {
                Ok(())
            } else {
                Err(format!(
                    "service {service:?} networks must contain network names"
                ))
            }
        }
        Value::Object(networks) => {
            for (network_name, options) in networks {
                if options.is_null() {
                    continue;
                }
                let options = options.as_object().ok_or_else(|| {
                    format!(
                        "service {service:?} network {network_name:?} options must be an object"
                    )
                })?;
                validate_keys(
                    options,
                    &["aliases"],
                    &format!("service {service:?} network {network_name:?}"),
                )?;
            }
            Ok(())
        }
        _ => Err(format!(
            "service {service:?} networks must be an object or array"
        )),
    }
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
    labels.retain(|key, _| {
        !key.starts_with("com.docker.compose.") && !key.starts_with("commitarium.")
    });
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
        if !allowed.contains(&key.as_str()) {
            return Err(format!("{name} uses unsupported field {key:?}"));
        }
    }
    Ok(())
}

fn validate_keys_with_extensions(
    object: &Map<String, Value>,
    allowed: &[&str],
    name: &str,
) -> Result<(), String> {
    for key in object.keys() {
        if !allowed.contains(&key.as_str()) && !key.starts_with("x-") {
            return Err(format!("{name} uses unsupported field {key:?}"));
        }
    }
    Ok(())
}

fn reject_normalized_interpolation(value: &Value) -> Result<(), String> {
    match value {
        Value::String(value) if value.contains('$') => Err(
            "the normalized Compose model cannot contain environment interpolation ('$')".into(),
        ),
        Value::Array(values) => {
            for value in values {
                reject_normalized_interpolation(value)?;
            }
            Ok(())
        }
        Value::Object(values) => {
            for (key, value) in values {
                if key.contains('$') {
                    return Err(
                        "the normalized Compose model cannot contain environment interpolation ('$')"
                            .into(),
                    );
                }
                reject_normalized_interpolation(value)?;
            }
            Ok(())
        }
        _ => Ok(()),
    }
}

fn owned_resource_name(project: &str, logical_name: &str) -> String {
    // Project IDs cannot contain '.', so the first dot is an unambiguous
    // boundary even when logical resource names contain underscores.
    format!("{project}.{logical_name}")
}

fn validate_raw_compose(path: &Path) -> Result<(), String> {
    let metadata = fs::metadata(path).map_err(|e| format!("inspect project Compose file: {e}"))?;
    if metadata.len() > MAX_COMPOSE_BYTES {
        return Err("the project Compose file is larger than 1 MiB".into());
    }
    let contents = fs::read(path).map_err(|e| format!("read project Compose file: {e}"))?;
    if contents.contains(&b'$') {
        return Err("the project Compose file cannot use environment interpolation ('$')".into());
    }
    let document: serde_yaml_ng::Value = serde_yaml_ng::from_slice(&contents)
        .map_err(|e| format!("parse project Compose file before validation: {e}"))?;
    validate_raw_document(&document)
}

fn validate_raw_document(value: &serde_yaml_ng::Value) -> Result<(), String> {
    let root = value
        .as_mapping()
        .ok_or_else(|| "the project Compose file must contain an object".to_string())?;
    for (key, value) in root {
        reject_raw_key(key, true)?;
        if key.as_str() == Some("services") {
            let services = value
                .as_mapping()
                .ok_or_else(|| "Compose services must be an object".to_string())?;
            for service in services.values() {
                reject_raw_file_keys(service, true, 0)?;
            }
        } else {
            reject_raw_file_keys(value, false, 0)?;
        }
    }
    Ok(())
}

fn reject_raw_file_keys(
    value: &serde_yaml_ng::Value,
    allow_extensions: bool,
    depth: usize,
) -> Result<(), String> {
    if depth > MAX_YAML_DEPTH {
        return Err("the project Compose file is nested too deeply".into());
    }
    match value {
        serde_yaml_ng::Value::Mapping(mapping) => {
            for (key, value) in mapping {
                reject_raw_key(key, allow_extensions)?;
                reject_raw_file_keys(value, false, depth + 1)?;
            }
        }
        serde_yaml_ng::Value::Sequence(sequence) => {
            for value in sequence {
                reject_raw_file_keys(value, false, depth + 1)?;
            }
        }
        serde_yaml_ng::Value::Tagged(tagged) => {
            reject_raw_file_keys(&tagged.value, allow_extensions, depth + 1)?;
        }
        _ => {}
    }
    Ok(())
}

fn reject_raw_key(key: &serde_yaml_ng::Value, allow_extensions: bool) -> Result<(), String> {
    let Some(key) = key.as_str() else {
        return Ok(());
    };
    if RAW_FILE_KEYS.contains(&key) {
        return Err(format!("the project Compose file cannot use {key:?}"));
    }
    if key.starts_with("x-") && !allow_extensions {
        return Err(format!(
            "Compose extension field {key:?} is allowed only at the document or service level"
        ));
    }
    Ok(())
}

pub(crate) fn sanitize_compose_command(command: &mut Command, safe_home: &Path) {
    let configured_path = command
        .get_envs()
        .find(|(key, _)| *key == "PATH")
        .and_then(|(_, value)| value.map(OsString::from))
        .or_else(|| std::env::var_os("PATH"));
    let docker_config = std::env::var_os("DOCKER_CONFIG").or_else(|| {
        std::env::var_os("HOME").map(|home| PathBuf::from(home).join(".docker").into_os_string())
    });

    command.env_clear();
    if let Some(path) = configured_path {
        command.env("PATH", path);
    }
    command.env("HOME", safe_home);
    if let Some(config) = docker_config {
        command.env("DOCKER_CONFIG", config);
    }
    for key in SAFE_DOCKER_ENV {
        if *key == "DOCKER_CONFIG" {
            continue;
        }
        if let Some(value) = std::env::var_os(key) {
            command.env(key, value);
        }
    }
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
            "x-project-note": "allowed at the top level",
            "services": {
                "web": {
                    "x-service-note": "allowed on a service",
                    "build": {"context": root.join("web"), "dockerfile": "Dockerfile", "args": {}, "target": "dev"},
                    "ports": [{"target": 5173, "published": "5173", "host_ip": "0.0.0.0", "protocol": "tcp"}],
                    "volumes": [{"type": "bind", "source": root.join("web"), "target": "/app", "bind": {"create_host_path": true}}],
                    "depends_on": {"api": {"condition": "service_started", "required": true}},
                    "networks": {"default": {"aliases": ["frontend"]}},
                    "labels": {"commitarium.preview": "victim", "com.docker.compose.project": "victim", "app.example.role": "frontend"}
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
            "volumes": {"data": {"name": "untrusted_data", "driver": "local", "labels": {"com.docker.compose.project": "victim"}}},
            "networks": {"default": {"name": "untrusted_default", "driver": "bridge", "labels": {"commitarium.preview": "victim"}}}
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
        assert!(web["labels"].get("com.docker.compose.project").is_none());
        assert_eq!(web["labels"]["app.example.role"], "frontend");
        assert_eq!(web["pids_limit"], 512);
        assert_eq!(
            prepared.model["volumes"]["data"]["name"],
            "commitarium-preview-prj_test.data"
        );
        assert!(prepared.model["volumes"]["data"].get("labels").is_none());
        assert_eq!(
            prepared.model["networks"]["default"]["name"],
            "commitarium-preview-prj_test.default"
        );
        assert!(prepared.model["networks"]["default"]
            .get("labels")
            .is_none());
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
            ("/networks/default", "attachable", json!(true)),
            ("/networks/default", "ipam", json!({"driver": "default"})),
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
    fn rejects_build_image_tag_and_unsafe_service_network_options() {
        let root = tempfile::tempdir().unwrap();
        let mut model = full_stack(root.path());
        model["services"]["web"]["image"] = json!("commitarium-coordinator:latest");
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
        assert!(error.contains("cannot set image when build is present"));

        for field in ["ipv4_address", "mac_address", "driver_opts"] {
            let root = tempfile::tempdir().unwrap();
            let mut model = full_stack(root.path());
            model["services"]["web"]["networks"] = json!({"default": {}});
            model["services"]["web"]["networks"]["default"]
                .as_object_mut()
                .unwrap()
                .insert(field.into(), json!("unsafe"));
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
    fn extensions_are_rejected_below_the_top_and_service_levels() {
        let root = tempfile::tempdir().unwrap();
        let mut model = full_stack(root.path());
        model["services"]["web"]["build"]["x-unsafe"] = json!(true);
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
        assert!(error.contains("x-unsafe"));
    }

    #[test]
    fn environment_variables_must_have_explicit_values() {
        for environment in [
            json!({"DOCKER_CONFIG": null}),
            json!(["PATH", "HOME=/preview/home"]),
            json!(["DOCKER_CONFIG"]),
        ] {
            let root = tempfile::tempdir().unwrap();
            let mut model = full_stack(root.path());
            model["services"]["web"]["environment"] = environment;
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
            assert!(
                error.contains("explicit value"),
                "unexpected error: {error}"
            );
        }

        let root = tempfile::tempdir().unwrap();
        let mut model = full_stack(root.path());
        model["services"]["web"]["environment"] = json!(["PATH=/preview/bin", "EMPTY="]);
        validate_and_rewrite(
            root.path(),
            "prj_test",
            &OpenTarget {
                service: "web".into(),
                port: 5173,
            },
            model,
        )
        .unwrap();
    }

    #[test]
    fn raw_compose_rejects_file_indirection_and_interpolation() {
        for (body, expected) in [
            ("services:\n  web:\n    env_file: /tmp/secret\n", "env_file"),
            (
                "services:\n  web: !custom { env_file: /tmp/secret }\n",
                "env_file",
            ),
            ("include: /tmp/compose.yaml\nservices: {}\n", "include"),
            (
                "services:\n  web:\n    extends:\n      file: /tmp/base.yaml\n      service: base\n",
                "extends",
            ),
            (
                "services:\n  web:\n    label_file: '/tmp/labels'\n",
                "label_file",
            ),
            (
                "services:\n  web:\n    image: '${HOME}/secret'\n",
                "interpolation",
            ),
            (
                "services:\n  web:\n    build:\n      context: .\n      x-unsafe: true\n",
                "allowed only",
            ),
        ] {
            let root = tempfile::tempdir().unwrap();
            let path = root.path().join("compose.yaml");
            fs::write(&path, body).unwrap();
            let error = validate_raw_compose(&path).unwrap_err();
            assert!(error.contains(expected), "error for {expected}: {error}");
        }
    }

    #[test]
    fn normalized_model_rejects_yaml_escaped_interpolation() {
        let root = tempfile::tempdir().unwrap();
        let path = root.path().join("compose.yaml");
        fs::write(
            &path,
            "services:\n  web:\n    environment:\n      LEAK: \"\\x24{DOCKER_CONFIG}\"\n",
        )
        .unwrap();

        // The source contains no literal '$', so the raw-byte defense cannot
        // see it. YAML decoding (and Compose normalization) produces one.
        validate_raw_compose(&path).unwrap();
        let yaml: serde_yaml_ng::Value =
            serde_yaml_ng::from_slice(&fs::read(&path).unwrap()).unwrap();
        let model = serde_json::to_value(yaml).unwrap();
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
        assert!(error.contains("interpolation"), "unexpected error: {error}");
    }

    #[test]
    fn normalized_model_rejects_dollars_in_paths_and_keys() {
        let root = tempfile::tempdir().unwrap();
        let escaped_path = root.path().join("${Z:-..}");
        fs::create_dir(&escaped_path).unwrap();
        let mut model = full_stack(root.path());
        model["services"]["web"]["volumes"][0]["source"] = json!(escaped_path);
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
        assert!(error.contains("interpolation"), "unexpected error: {error}");

        let mut model = full_stack(root.path());
        model["services"]["web"]["environment"] = json!({"$SECRET": "value"});
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
        assert!(error.contains("interpolation"), "unexpected error: {error}");
    }

    #[test]
    fn resource_names_have_an_unambiguous_project_boundary() {
        assert_ne!(
            owned_resource_name("commitarium-preview-zzh", "a_data"),
            owned_resource_name("commitarium-preview-zzh_a", "data")
        );
        assert_eq!(
            owned_resource_name("commitarium-preview-prj_test", "data"),
            "commitarium-preview-prj_test.data"
        );
    }

    #[test]
    fn compose_subprocess_environment_is_allowlisted() {
        let root = tempfile::tempdir().unwrap();
        let mut command = Command::new("env");
        command.env("PATH", "/safe/bin");
        command.env("AWS_SECRET_ACCESS_KEY", "must-not-survive");
        sanitize_compose_command(&mut command, root.path());
        let environment = command
            .get_envs()
            .map(|(key, _)| key.to_string_lossy().into_owned())
            .collect::<Vec<_>>();
        assert!(environment.contains(&"HOME".to_string()));
        assert!(environment.contains(&"PATH".to_string()));
        assert!(!environment.contains(&"AWS_SECRET_ACCESS_KEY".to_string()));
        assert!(environment.iter().all(|key| {
            key == "HOME" || key == "PATH" || SAFE_DOCKER_ENV.contains(&key.as_str())
        }));
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

    #[cfg(unix)]
    #[test]
    fn rejects_a_symlinked_root_compose_file_before_reading_it() {
        use std::os::unix::fs::symlink;
        let parent = tempfile::tempdir().unwrap();
        let root = parent.path().join("repo");
        fs::create_dir(&root).unwrap();
        let outside = parent.path().join("secret.yaml");
        fs::write(&outside, "host secret").unwrap();
        symlink(&outside, root.join("compose.yaml")).unwrap();

        let error = find_compose_file(&root).unwrap_err();
        assert!(error.contains("symbolic link"));
    }
}
