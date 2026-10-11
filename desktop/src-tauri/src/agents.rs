//! Agents (ADR-016): one worker per provider account.
//!
//! The coordinator owns the agent list; the desktop owns each agent's
//! credentials and runs its worker. Every agent gets a generated Compose
//! service, `agent-<id>-worker`, built from its provider's template with the
//! agent's private provider volume, journal, bearer token, both Forgejo
//! tokens, and both workspace trees. Nothing here reads provider credentials.

use serde::{Deserialize, Serialize};
use serde_json::{json, Map, Value};
use std::fs;
use std::io::Write;
use std::path::PathBuf;
use std::thread;
use std::time::Duration;

use crate::{bootstrap, docker, phase4};

const AGENTS_COMPOSE_ENV: &str = "COMMITARIUM_AGENTS_COMPOSE_FILE";
const TEMPLATE_PROFILE: &str = "agent-template";
const MAX_ID_LENGTH: usize = 30;
const LIST_ATTEMPTS: usize = 120;
const LIST_DELAY: Duration = Duration::from_millis(500);
const LIST_TIMEOUT: Duration = Duration::from_secs(5);

#[derive(Clone, Copy, Debug, Deserialize, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub(crate) enum Provider {
    Codex,
    Claude,
}

impl Provider {
    fn env_prefix(self) -> &'static str {
        match self {
            Provider::Codex => "COMMITARIUM_CODEX",
            Provider::Claude => "COMMITARIUM_CLAUDE",
        }
    }

    /// The Compose service the provider's agent workers are generated from.
    pub(crate) fn template_service(self) -> &'static str {
        match self {
            Provider::Codex => "codex-agent-template",
            Provider::Claude => "claude-agent-template",
        }
    }

    pub(crate) fn executable(self) -> &'static str {
        match self {
            Provider::Codex => "codex",
            Provider::Claude => "claude",
        }
    }
}

#[derive(Clone, Debug, Deserialize, Eq, PartialEq, Serialize)]
pub(crate) struct Agent {
    pub(crate) id: String,
    pub(crate) name: String,
    pub(crate) provider: Provider,
}

impl Agent {
    #[cfg(test)]
    pub(crate) fn new(id: &str, name: &str, provider: Provider) -> Self {
        Self {
            id: id.to_string(),
            name: name.to_string(),
            provider,
        }
    }
}

/// Agent IDs are lowercase letters, digits, and dashes (at most 30), the same
/// rule the coordinator enforces, so every derived name below stays valid.
pub(crate) fn valid_id(id: &str) -> bool {
    let bytes = id.as_bytes();
    !bytes.is_empty()
        && bytes.len() <= MAX_ID_LENGTH
        && bytes
            .iter()
            .all(|byte| byte.is_ascii_lowercase() || byte.is_ascii_digit() || *byte == b'-')
        && bytes[0] != b'-'
        && bytes[bytes.len() - 1] != b'-'
}

pub(crate) fn worker_service(agent_id: &str) -> String {
    format!("agent-{agent_id}-worker")
}

pub(crate) fn worker_token_file(agent_id: &str) -> String {
    format!("agent-{agent_id}-worker-token")
}

pub(crate) fn login_container(agent_id: &str) -> String {
    format!("commitarium-login-{agent_id}")
}

/// The provider-state and journal volumes. The `codex` and `claude` agents
/// keep the volumes of the lead profiles they were migrated from, so their
/// logins and journals carry over without copying.
pub(crate) fn volumes(agent_id: &str) -> (String, String) {
    match agent_id {
        "codex" | "claude" => (
            format!("{agent_id}-profile"),
            format!("{agent_id}-worker-journal"),
        ),
        _ => (
            format!("agent-{agent_id}-profile"),
            format!("agent-{agent_id}-journal"),
        ),
    }
}

#[derive(Deserialize)]
struct ListedAgent {
    id: String,
    name: String,
    provider: String,
}

/// Fetch the agents from the coordinator. With `wait`, keep trying while the
/// coordinator starts. Agents with an unexpected ID or provider are rejected
/// rather than turned into Compose names.
pub(crate) fn fetch(wait: bool) -> Result<Vec<Agent>, String> {
    let attempts = if wait { LIST_ATTEMPTS } else { 1 };
    let mut last_error = String::new();
    for attempt in 0..attempts {
        if attempt > 0 {
            thread::sleep(LIST_DELAY);
        }
        match tauri::async_runtime::block_on(fetch_once()) {
            Ok(agents) => {
                save_cache(&agents);
                return Ok(agents);
            }
            Err(error) => last_error = error,
        }
    }
    Err(last_error)
}

async fn fetch_once() -> Result<Vec<Agent>, String> {
    let response = reqwest::Client::builder()
        .timeout(LIST_TIMEOUT)
        .build()
        .map_err(|_| "could not create the coordinator client".to_string())?
        .get(phase4::coordinator_url("/api/v1/agents"))
        .send()
        .await
        .map_err(|_| "the coordinator is unavailable".to_string())?;
    if !response.status().is_success() {
        return Err(format!(
            "the coordinator could not list agents (HTTP {})",
            response.status()
        ));
    }
    let listed: Vec<ListedAgent> = response
        .json()
        .await
        .map_err(|_| "the coordinator returned an invalid agent list".to_string())?;
    listed
        .into_iter()
        .map(|agent| {
            let provider = match agent.provider.as_str() {
                "codex" => Provider::Codex,
                "claude" => Provider::Claude,
                _ => return Err(format!("agent {} uses an unsupported provider", agent.id)),
            };
            if !valid_id(&agent.id) || agent.name.contains(['\r', '\n']) {
                return Err("the coordinator returned an invalid agent".to_string());
            }
            Ok(Agent {
                id: agent.id,
                name: agent.name,
                provider,
            })
        })
        .collect()
}

/// The agents from the coordinator, or the last list seen when it is not
/// running (the stack may be stopped while the user manages logins).
pub(crate) fn current() -> Result<Vec<Agent>, String> {
    fetch(false).or_else(|error| load_cache().ok_or(error))
}

fn overlay_path() -> Option<PathBuf> {
    std::env::var_os(AGENTS_COMPOSE_ENV).map(PathBuf::from)
}

fn cache_path() -> Option<PathBuf> {
    overlay_path().map(|path| path.with_file_name("agents.json"))
}

fn save_cache(agents: &[Agent]) {
    if let (Some(path), Ok(contents)) = (cache_path(), serde_json::to_vec(agents)) {
        let _ = write_atomic(&path, &contents);
    }
}

fn load_cache() -> Option<Vec<Agent>> {
    let contents = fs::read(cache_path()?).ok()?;
    let agents: Vec<Agent> = serde_json::from_slice(&contents).ok()?;
    agents
        .iter()
        .all(|agent| valid_id(&agent.id))
        .then_some(agents)
}

/// Point Compose at the generated agents overlay in the runtime directory.
pub(crate) fn init_overlay_path(runtime_dir: &std::path::Path) {
    if std::env::var_os(AGENTS_COMPOSE_ENV).is_none() {
        std::env::set_var(AGENTS_COMPOSE_ENV, runtime_dir.join("compose.agents.yml"));
    }
}

/// The generated overlay, when it exists, for the Compose file set.
pub(crate) fn existing_overlay() -> Option<PathBuf> {
    overlay_path().filter(|path| path.exists())
}

/// Whether the overlay defines exactly these agents' workers.
pub(crate) fn overlay_matches(agents: &[Agent]) -> bool {
    let Some(contents) = overlay_path().and_then(|path| fs::read(path).ok()) else {
        return false;
    };
    let Ok(overlay) = serde_json::from_slice::<Value>(&contents) else {
        return false;
    };
    let Some(services) = overlay.get("services").and_then(Value::as_object) else {
        return false;
    };
    services.len() == agents.len()
        && agents
            .iter()
            .all(|agent| services.contains_key(&worker_service(&agent.id)))
}

/// Regenerate the overlay from the provider templates. Returns whether it
/// changed. Agent IDs are validated, and every copied value has `$` escaped,
/// so nothing in the overlay is interpreted by Compose interpolation except
/// the fixed source paths written here.
pub(crate) fn write_overlay(agents: &[Agent]) -> Result<bool, String> {
    let path =
        overlay_path().ok_or_else(|| "agents Compose path was not initialized".to_string())?;
    let resolved = docker::template_config(TEMPLATE_PROFILE)?;
    let templates: Value = serde_json::from_str(&resolved)
        .map_err(|e| format!("decode agent worker templates: {e}"))?;
    let overlay = overlay(agents, &templates)?;
    let contents = serde_json::to_vec_pretty(&overlay).map_err(|e| e.to_string())?;
    if fs::read(&path).is_ok_and(|current| current == contents) {
        return Ok(false);
    }
    write_atomic(&path, &contents)?;
    Ok(true)
}

fn overlay(agents: &[Agent], templates: &Value) -> Result<Value, String> {
    let mut services = Map::new();
    let mut declared_volumes = Map::new();
    for agent in agents {
        if !valid_id(&agent.id) {
            return Err(format!("agent ID {:?} is invalid", agent.id));
        }
        let template = templates
            .get("services")
            .and_then(|services| services.get(agent.provider.template_service()))
            .and_then(Value::as_object)
            .ok_or_else(|| {
                format!(
                    "the {} agent worker template is missing",
                    agent.provider.template_service()
                )
            })?;
        services.insert(worker_service(&agent.id), agent_service(agent, template));
        let (profile, journal) = volumes(&agent.id);
        declared_volumes.insert(profile, json!({}));
        declared_volumes.insert(journal, json!({}));
    }
    Ok(json!({ "services": services, "volumes": declared_volumes }))
}

fn agent_service(agent: &Agent, template: &Map<String, Value>) -> Value {
    let mut service: Map<String, Value> = template
        .iter()
        .filter(|(key, value)| {
            !value.is_null() && !matches!(key.as_str(), "profiles" | "container_name")
        })
        .map(|(key, value)| (key.clone(), escape_interpolation(value)))
        .collect();

    let prefix = agent.provider.env_prefix();
    let id = &agent.id;
    let name = agent.name.replace('$', "$$");
    let mut environment = service
        .remove("environment")
        .and_then(|value| value.as_object().cloned())
        .unwrap_or_default();
    let mut set = |key: String, value: String| {
        environment.insert(key, Value::String(value));
    };
    set(
        "COMMITARIUM_WORKER_DATABASE_PATH".into(),
        "/var/lib/commitarium-worker/worker.db".into(),
    );
    set("COMMITARIUM_WORKER_LISTEN_ADDRESS".into(), ":8081".into());
    set(
        "COMMITARIUM_WORKER_TOKEN_FILE".into(),
        "/run/commitarium-internal/worker-token".into(),
    );
    set(
        "COMMITARIUM_COORDINATOR_URL".into(),
        "http://coordinator:8080".into(),
    );
    set(
        "COMMITARIUM_TOOLCHAIN_ROOT".into(),
        "/var/lib/commitarium-toolchains".into(),
    );
    set(
        format!("{prefix}_PROVIDER_STATE_PATH"),
        "/var/lib/commitarium-provider".into(),
    );
    set(format!("{prefix}_WORKSPACE_ROOT"), "/workspaces".into());
    set(format!("{prefix}_PROFILE_ID"), id.clone());
    set(
        format!("{prefix}_FORGEJO_URL"),
        "http://forgejo:3000".into(),
    );
    for (role, label, root) in [
        ("lead", "Lead", "/workspaces"),
        ("reviewer", "Reviewer", "/reviewer-workspaces"),
    ] {
        let settings = format!("{prefix}_{}", role.to_ascii_uppercase());
        set(
            format!("{settings}_FORGEJO_TOKEN_FILE"),
            format!("/run/commitarium-agent/{role}-forgejo-token"),
        );
        set(format!("{settings}_FORGEJO_LOGIN"), format!("{id}-{role}"));
        set(
            format!("{settings}_GIT_AUTHOR_NAME"),
            format!("Commitarium {name} {label}"),
        );
        set(
            format!("{settings}_GIT_AUTHOR_EMAIL"),
            format!("{id}-{role}@commitarium.local"),
        );
        set(format!("{settings}_WORKSPACE_ROOT"), root.into());
    }
    service.insert("environment".into(), Value::Object(environment));

    let (profile, journal) = volumes(id);
    let bind = |source: String, target: &str, read_only: bool| json!({ "type": "bind", "source": source, "target": target, "read_only": read_only });
    service.insert(
        "volumes".into(),
        json!([
            { "type": "volume", "source": journal, "target": "/var/lib/commitarium-worker" },
            { "type": "volume", "source": profile, "target": "/var/lib/commitarium-provider" },
            bind(
                format!(
                    "${{COMMITARIUM_AGENT_WORKER_TOKEN_DIR_SOURCE:-./.commitarium/internal/agent-workers}}/{}",
                    worker_token_file(id)
                ),
                "/run/commitarium-internal/worker-token",
                true,
            ),
            bind(
                format!("./{}", bootstrap::agent_forgejo_token_relative_path(id, "lead")),
                "/run/commitarium-agent/lead-forgejo-token",
                true,
            ),
            bind(
                format!("./{}", bootstrap::agent_forgejo_token_relative_path(id, "reviewer")),
                "/run/commitarium-agent/reviewer-forgejo-token",
                true,
            ),
            bind(
                "${COMMITARIUM_WORKSPACE_SOURCE:-./.commitarium/workspaces}".into(),
                "/workspaces",
                false,
            ),
            bind(
                "${COMMITARIUM_REVIEWER_WORKSPACE_SOURCE:-./.commitarium/reviewer-workspaces}".into(),
                "/reviewer-workspaces",
                false,
            ),
            { "type": "volume", "source": "commitarium-toolchains", "target": "/var/lib/commitarium-toolchains" },
        ]),
    );
    Value::Object(service)
}

/// Escape `$` in values taken from the resolved templates; Compose already
/// interpolated them once.
fn escape_interpolation(value: &Value) -> Value {
    match value {
        Value::String(text) => Value::String(text.replace('$', "$$")),
        Value::Array(items) => Value::Array(items.iter().map(escape_interpolation).collect()),
        Value::Object(fields) => Value::Object(
            fields
                .iter()
                .map(|(key, value)| (key.clone(), escape_interpolation(value)))
                .collect(),
        ),
        other => other.clone(),
    }
}

fn write_atomic(path: &std::path::Path, contents: &[u8]) -> Result<(), String> {
    let parent = path
        .parent()
        .ok_or_else(|| "agents Compose path has no parent".to_string())?;
    fs::create_dir_all(parent).map_err(|e| format!("create agents Compose directory: {e}"))?;
    let mut temporary = tempfile::NamedTempFile::new_in(parent)
        .map_err(|e| format!("create agents Compose update: {e}"))?;
    temporary
        .write_all(contents)
        .map_err(|e| format!("write agents Compose update: {e}"))?;
    temporary
        .persist(path)
        .map_err(|e| format!("install agents Compose update: {e}"))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn templates() -> Value {
        json!({ "services": {
            "codex-agent-template": {
                "profiles": ["agent-template"],
                "image": "ghcr.io/example/codex:1",
                "command": null,
                "environment": { "COMMITARIUM_WORKER_ADAPTER": "codex", "COMMITARIUM_CODEX_MODEL": "a$b" },
                "security_opt": ["seccomp=unconfined"],
            },
            "claude-agent-template": {
                "profiles": ["agent-template"],
                "image": "ghcr.io/example/claude:1",
                "environment": { "COMMITARIUM_WORKER_ADAPTER": "claude_code" },
            },
        }})
    }

    #[test]
    fn agent_ids_follow_the_coordinator_rule() {
        assert!(valid_id("claude-max-2"));
        assert!(!valid_id("Claude"));
        assert!(!valid_id("-claude"));
        assert!(!valid_id("claude/../x"));
        assert!(!valid_id(&"a".repeat(31)));
    }

    #[test]
    fn migrated_agents_keep_their_lead_profile_volumes() {
        assert_eq!(
            volumes("codex"),
            ("codex-profile".into(), "codex-worker-journal".into())
        );
        assert_eq!(
            volumes("claude-max"),
            (
                "agent-claude-max-profile".into(),
                "agent-claude-max-journal".into()
            )
        );
    }

    #[test]
    fn overlay_gives_each_agent_a_worker_serving_both_roles() {
        let agents = [
            Agent::new("codex", "Codex", Provider::Codex),
            Agent::new("claude-max", "Claude $Max", Provider::Claude),
        ];
        let overlay = overlay(&agents, &templates()).unwrap();
        let codex = &overlay["services"]["agent-codex-worker"];
        assert_eq!(codex["image"], "ghcr.io/example/codex:1");
        assert!(codex.get("profiles").is_none() && codex.get("command").is_none());
        assert_eq!(codex["security_opt"][0], "seccomp=unconfined");
        let environment = &codex["environment"];
        assert_eq!(environment["COMMITARIUM_CODEX_MODEL"], "a$$b");
        assert_eq!(environment["COMMITARIUM_CODEX_PROFILE_ID"], "codex");
        assert_eq!(
            environment["COMMITARIUM_CODEX_LEAD_FORGEJO_LOGIN"],
            "codex-lead"
        );
        assert_eq!(
            environment["COMMITARIUM_CODEX_REVIEWER_FORGEJO_LOGIN"],
            "codex-reviewer"
        );
        assert_eq!(
            environment["COMMITARIUM_CODEX_REVIEWER_WORKSPACE_ROOT"],
            "/reviewer-workspaces"
        );
        assert!(environment.get("COMMITARIUM_CODEX_FORGEJO_ROLE").is_none());
        assert_eq!(codex["volumes"][1]["source"], "codex-profile");

        let claude = &overlay["services"]["agent-claude-max-worker"];
        assert_eq!(
            claude["environment"]["COMMITARIUM_CLAUDE_LEAD_GIT_AUTHOR_NAME"],
            "Commitarium Claude $$Max Lead"
        );
        assert_eq!(
            claude["volumes"][2]["source"],
            "${COMMITARIUM_AGENT_WORKER_TOKEN_DIR_SOURCE:-./.commitarium/internal/agent-workers}/agent-claude-max-worker-token"
        );
        assert_eq!(
            claude["volumes"][4]["source"],
            "./.commitarium/agents/claude-max-reviewer/forgejo-token"
        );
        assert!(overlay["volumes"].get("agent-claude-max-journal").is_some());
    }

    #[test]
    fn overlay_rejects_unsafe_ids_and_missing_templates() {
        let unsafe_agent = [Agent::new("../x", "X", Provider::Codex)];
        assert!(overlay(&unsafe_agent, &templates()).is_err());
        let agents = [Agent::new("codex", "Codex", Provider::Codex)];
        assert!(overlay(&agents, &json!({ "services": {} })).is_err());
    }
}
