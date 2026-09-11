//! Provider-profile authentication owned by the trusted desktop backend.
//!
//! Provider credentials never cross into the coordinator. Login commands run
//! inside the exact lead/reviewer service whose private provider-state volume
//! will later be used for agent work. Only deliberately parsed progress facts
//! (a trusted browser URL, a one-time device code, or a generic status) are
//! emitted to the frontend; raw provider CLI output is never forwarded.

use serde::Serialize;
use std::collections::HashMap;
use std::io::{Read, Write};
use std::process::{Child, ChildStdin, Command, ExitStatus, Stdio};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::thread;
use std::time::Duration;
use tauri::{AppHandle, Emitter, State};
use zeroize::Zeroizing;

use crate::docker;

const PROGRESS_EVENT: &str = "login_progress";
const OUTPUT_LIMIT: usize = 64 * 1024;
const POLL_INTERVAL: Duration = Duration::from_millis(50);

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
enum Provider {
    Codex,
    Claude,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
enum Role {
    Lead,
    Reviewer,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
pub enum ProfileStatus {
    NotConfigured,
    Starting,
    WaitingForBrowser,
    WaitingForCode,
    WaitingForApiKey,
    Verifying,
    Connected,
    Expired,
    Failed,
}

#[derive(Clone, Debug, Default, Eq, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct ProfileDetail {
    #[serde(skip_serializing_if = "Option::is_none")]
    message: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    browser_url: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    device_code: Option<String>,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Profile {
    id: String,
    provider: Provider,
    role: Role,
    status: ProfileStatus,
    #[serde(skip_serializing_if = "Option::is_none")]
    detail: Option<ProfileDetail>,
}

#[derive(Clone, Debug, Serialize)]
#[serde(rename_all = "camelCase")]
struct LoginProgress {
    profile_id: String,
    status: ProfileStatus,
    #[serde(skip_serializing_if = "Option::is_none")]
    detail: Option<ProfileDetail>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum LoginMethod {
    Subscription,
    ApiKey,
}

impl LoginMethod {
    fn parse(value: &str) -> Result<Self, String> {
        match value {
            "subscription" => Ok(Self::Subscription),
            "api_key" => Ok(Self::ApiKey),
            _ => Err("login method must be subscription or api_key".to_string()),
        }
    }
}

#[derive(Clone, Copy, Debug)]
struct ProfileSpec {
    id: &'static str,
    provider: Provider,
    role: Role,
    compose_profile: &'static str,
    service: &'static str,
    executable: &'static str,
    login_container: &'static str,
}

/// The small part of a provider profile that the stack launcher needs.
///
/// Authentication remains owned by this module. The launcher receives only a
/// yes/no decision plus fixed Compose identifiers, never provider credentials
/// or provider command output.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) struct WorkerConnection {
    pub(crate) service: &'static str,
    pub(crate) connected: bool,
}

const PROFILES: &[ProfileSpec] = &[
    ProfileSpec {
        id: "codex-lead",
        provider: Provider::Codex,
        role: Role::Lead,
        compose_profile: "real-codex",
        service: "codex-worker",
        executable: "codex",
        login_container: "commitarium-login-codex-lead",
    },
    ProfileSpec {
        id: "codex-reviewer",
        provider: Provider::Codex,
        role: Role::Reviewer,
        compose_profile: "real-codex",
        service: "codex-reviewer-worker",
        executable: "codex",
        login_container: "commitarium-login-codex-reviewer",
    },
    ProfileSpec {
        id: "claude-lead",
        provider: Provider::Claude,
        role: Role::Lead,
        compose_profile: "real-claude",
        service: "claude-worker",
        executable: "claude",
        login_container: "commitarium-login-claude-lead",
    },
    ProfileSpec {
        id: "claude-reviewer",
        provider: Provider::Claude,
        role: Role::Reviewer,
        compose_profile: "real-claude",
        service: "claude-reviewer-worker",
        executable: "claude",
        login_container: "commitarium-login-claude-reviewer",
    },
];

#[derive(Clone)]
struct ActiveLogin {
    generation: u64,
    method: LoginMethod,
    submitted: bool,
    child: Option<Arc<Mutex<Child>>>,
    stdin: Option<Arc<Mutex<Option<ChildStdin>>>>,
    cancelled: Arc<AtomicBool>,
}

struct ProfileState {
    statuses: HashMap<&'static str, (ProfileStatus, Option<ProfileDetail>)>,
    active: HashMap<&'static str, ActiveLogin>,
}

struct ProfileManagerInner {
    state: Mutex<ProfileState>,
    next_generation: AtomicU64,
}

/// Shared in-memory login-process registry. Credentials themselves are never
/// stored here; only process handles and non-secret progress state are kept.
#[derive(Clone)]
pub struct ProfileManager {
    inner: Arc<ProfileManagerInner>,
}

impl ProfileManager {
    pub fn new() -> Self {
        let statuses = PROFILES
            .iter()
            .map(|spec| (spec.id, (ProfileStatus::NotConfigured, None)))
            .collect();
        Self {
            inner: Arc::new(ProfileManagerInner {
                state: Mutex::new(ProfileState {
                    statuses,
                    active: HashMap::new(),
                }),
                next_generation: AtomicU64::new(1),
            }),
        }
    }

    fn snapshot(&self, spec: ProfileSpec) -> Profile {
        let state = self.inner.state.lock().expect("profile state poisoned");
        let (status, detail) = state
            .statuses
            .get(spec.id)
            .cloned()
            .unwrap_or((ProfileStatus::NotConfigured, None));
        profile(spec, status, detail)
    }

    fn is_active(&self, profile_id: &str) -> bool {
        self.inner
            .state
            .lock()
            .expect("profile state poisoned")
            .active
            .contains_key(profile_id)
    }

    fn reserve(
        &self,
        spec: ProfileSpec,
        method: LoginMethod,
        status: ProfileStatus,
    ) -> Result<u64, String> {
        let mut state = self.inner.state.lock().expect("profile state poisoned");
        if state.active.contains_key(spec.id) {
            return Err("a login is already in progress for this profile".to_string());
        }
        let generation = self.inner.next_generation.fetch_add(1, Ordering::Relaxed);
        state.active.insert(
            spec.id,
            ActiveLogin {
                generation,
                method,
                submitted: false,
                child: None,
                stdin: None,
                cancelled: Arc::new(AtomicBool::new(false)),
            },
        );
        state.statuses.insert(spec.id, (status, None));
        Ok(generation)
    }

    fn attach_process(
        &self,
        spec: ProfileSpec,
        generation: u64,
        child: Arc<Mutex<Child>>,
        stdin: Arc<Mutex<Option<ChildStdin>>>,
    ) -> Result<Arc<AtomicBool>, String> {
        let mut state = self.inner.state.lock().expect("profile state poisoned");
        let active = state
            .active
            .get_mut(spec.id)
            .filter(|active| active.generation == generation)
            .ok_or_else(|| "the login was cancelled before it started".to_string())?;
        active.child = Some(child);
        active.stdin = Some(stdin);
        Ok(active.cancelled.clone())
    }

    fn mark_submitted(&self, spec: ProfileSpec, generation: u64) -> Result<(), String> {
        let mut state = self.inner.state.lock().expect("profile state poisoned");
        let active = state
            .active
            .get_mut(spec.id)
            .filter(|active| active.generation == generation)
            .ok_or_else(|| "the login is no longer active".to_string())?;
        active.submitted = true;
        Ok(())
    }

    fn active(&self, spec: ProfileSpec) -> Option<ActiveLogin> {
        self.inner
            .state
            .lock()
            .expect("profile state poisoned")
            .active
            .get(spec.id)
            .cloned()
    }

    fn finish(&self, spec: ProfileSpec, generation: u64) {
        let mut state = self.inner.state.lock().expect("profile state poisoned");
        if state
            .active
            .get(spec.id)
            .is_some_and(|active| active.generation == generation)
        {
            state.active.remove(spec.id);
        }
    }

    fn transition(
        &self,
        app: &AppHandle,
        spec: ProfileSpec,
        status: ProfileStatus,
        detail: Option<ProfileDetail>,
    ) {
        self.inner
            .state
            .lock()
            .expect("profile state poisoned")
            .statuses
            .insert(spec.id, (status, detail.clone()));
        let _ = app.emit(
            PROGRESS_EVENT,
            LoginProgress {
                profile_id: spec.id.to_string(),
                status,
                detail,
            },
        );
    }

    /// Stop only the fixed, Commitarium-owned authentication processes. This
    /// runs when the desktop application exits so an abandoned login cannot
    /// keep a private profile volume mounted behind the next app launch.
    pub fn shutdown(&self) {
        let active: Vec<_> = self
            .inner
            .state
            .lock()
            .expect("profile state poisoned")
            .active
            .iter()
            .filter_map(|(profile_id, login)| {
                profile_spec(profile_id)
                    .ok()
                    .map(|spec| (spec, login.clone()))
            })
            .collect();
        for (spec, login) in active {
            login.cancelled.store(true, Ordering::Release);
            if let Some(child) = login.child {
                let _ = child.lock().expect("login process poisoned").kill();
            }
            remove_login_container(spec);
        }
    }
}

impl Default for ProfileManager {
    fn default() -> Self {
        Self::new()
    }
}

fn profile(spec: ProfileSpec, status: ProfileStatus, detail: Option<ProfileDetail>) -> Profile {
    Profile {
        id: spec.id.to_string(),
        provider: spec.provider,
        role: spec.role,
        status,
        detail,
    }
}

fn profile_spec(profile_id: &str) -> Result<ProfileSpec, String> {
    PROFILES
        .iter()
        .copied()
        .find(|spec| spec.id == profile_id)
        .ok_or_else(|| "unknown provider profile".to_string())
}

fn paired_spec(spec: ProfileSpec) -> ProfileSpec {
    PROFILES
        .iter()
        .copied()
        .find(|candidate| candidate.provider == spec.provider && candidate.role != spec.role)
        .expect("each provider has a paired role")
}

#[tauri::command]
pub async fn list_profiles(manager: State<'_, ProfileManager>) -> Result<Vec<Profile>, String> {
    let manager = manager.inner().clone();
    tauri::async_runtime::spawn_blocking(move || inspect_profiles(&manager))
        .await
        .map_err(|_| "could not inspect provider profiles".to_string())
}

/// Verify all role profiles and return only the fixed routing facts needed by
/// stack startup. Active login sessions are deliberately not re-probed: their
/// private volume is already owned by the login helper, and their in-memory
/// non-connected state keeps the long-lived worker stopped.
pub(crate) fn worker_connections(manager: &ProfileManager) -> Vec<WorkerConnection> {
    inspect_profiles(manager)
        .into_iter()
        .zip(PROFILES.iter().copied())
        .map(|(profile, spec)| WorkerConnection {
            service: spec.service,
            connected: profile.status == ProfileStatus::Connected,
        })
        .collect()
}

fn inspect_profiles(manager: &ProfileManager) -> Vec<Profile> {
    let checks: Vec<_> = PROFILES
        .iter()
        .copied()
        .map(|spec| {
            let manager = manager.clone();
            thread::spawn(move || {
                if manager.is_active(spec.id) {
                    manager.snapshot(spec)
                } else {
                    probe_profile(spec)
                }
            })
        })
        .collect();
    checks
        .into_iter()
        .zip(PROFILES.iter().copied())
        .map(|(check, spec)| {
            check.join().unwrap_or_else(|_| {
                profile(
                    spec,
                    ProfileStatus::Failed,
                    Some(detail("The provider profile could not be inspected.")),
                )
            })
        })
        .collect()
}

#[tauri::command]
pub fn begin_login(
    app: AppHandle,
    manager: State<'_, ProfileManager>,
    profile_id: String,
    method: String,
) -> Result<(), String> {
    let spec = profile_spec(&profile_id)?;
    let method = LoginMethod::parse(&method)?;
    if service_is_running(spec)? {
        return Err(
            "this provider worker is running; stop the stack before changing its login".to_string(),
        );
    }
    match method {
        LoginMethod::ApiKey => {
            let manager = manager.inner().clone();
            manager.reserve(spec, method, ProfileStatus::WaitingForApiKey)?;
            manager.transition(
                &app,
                spec,
                ProfileStatus::WaitingForApiKey,
                Some(detail("Enter the API key to connect this profile.")),
            );
            Ok(())
        }
        LoginMethod::Subscription => start_subscription(app, manager.inner().clone(), spec),
    }
}

#[tauri::command]
pub fn submit_login_code(
    manager: State<'_, ProfileManager>,
    profile_id: String,
    code: String,
) -> Result<(), String> {
    let spec = profile_spec(&profile_id)?;
    if spec.provider != Provider::Claude {
        return Err("Codex device codes are entered in the browser".to_string());
    }
    let code = Zeroizing::new(code);
    if code.trim().is_empty() || code.len() > 4096 {
        return Err("login code must be between 1 and 4096 characters".to_string());
    }
    let active = manager
        .active(spec)
        .filter(|active| active.method == LoginMethod::Subscription)
        .ok_or_else(|| "this profile is not waiting for a login code".to_string())?;
    let stdin = active
        .stdin
        .ok_or_else(|| "the provider login is not ready for a code yet".to_string())?;
    let mut slot = stdin.lock().expect("login stdin poisoned");
    let mut input = slot
        .take()
        .ok_or_else(|| "the login code was already submitted".to_string())?;
    input
        .write_all(code.trim().as_bytes())
        .and_then(|_| input.write_all(b"\n"))
        .map_err(|_| "could not send the login code to the provider".to_string())?;
    input
        .flush()
        .map_err(|_| "could not send the login code to the provider".to_string())?;
    Ok(())
}

#[tauri::command]
pub fn submit_api_key(
    app: AppHandle,
    manager: State<'_, ProfileManager>,
    profile_id: String,
    key: String,
    use_for_both_roles: Option<bool>,
) -> Result<(), String> {
    let spec = profile_spec(&profile_id)?;
    let key = Zeroizing::new(key.into_bytes());
    if key.is_empty() || key.len() > 16 * 1024 || key.iter().any(u8::is_ascii_whitespace) {
        return Err("API key must be one non-empty value without whitespace".to_string());
    }
    let manager = manager.inner().clone();
    let active = manager
        .active(spec)
        .filter(|active| active.method == LoginMethod::ApiKey)
        .ok_or_else(|| "begin API-key login for this profile first".to_string())?;

    let mut targets = vec![(spec, active.generation)];
    if use_for_both_roles.unwrap_or(false) {
        let paired = paired_spec(spec);
        if service_is_running(paired)? {
            return Err(
                "the paired provider worker is running; stop the stack before changing its login"
                    .to_string(),
            );
        }
        let generation = manager.reserve(paired, LoginMethod::ApiKey, ProfileStatus::Starting)?;
        targets.push((paired, generation));
    }

    for (target, generation) in &targets {
        manager.mark_submitted(*target, *generation)?;
    }

    for (target, generation) in targets {
        let app = app.clone();
        let manager = manager.clone();
        let target_key = Zeroizing::new(key.to_vec());
        thread::spawn(move || provision_api_key(app, manager, target, generation, target_key));
    }
    Ok(())
}

#[tauri::command]
pub fn cancel_login(
    app: AppHandle,
    manager: State<'_, ProfileManager>,
    profile_id: String,
) -> Result<(), String> {
    let spec = profile_spec(&profile_id)?;
    let active = manager
        .active(spec)
        .ok_or_else(|| "no login is in progress for this profile".to_string())?;
    active.cancelled.store(true, Ordering::Release);
    if let Some(child) = active.child {
        let _ = child.lock().expect("login process poisoned").kill();
        remove_login_container(spec);
        manager.transition(
            &app,
            spec,
            ProfileStatus::Verifying,
            Some(detail("Cancelling login and checking the profile.")),
        );
    } else if !active.submitted {
        manager.transition(
            &app,
            spec,
            ProfileStatus::Verifying,
            Some(detail("Cancelling login and checking the profile.")),
        );
        let manager = manager.inner().clone();
        thread::spawn(move || {
            let current = probe_profile(spec);
            manager.transition(
                &app,
                spec,
                current.status,
                Some(ProfileDetail {
                    message: Some(
                        "Login cancelled; any previous profile login was preserved.".to_string(),
                    ),
                    ..current.detail.unwrap_or_default()
                }),
            );
            manager.finish(spec, active.generation);
        });
    } else {
        manager.transition(
            &app,
            spec,
            ProfileStatus::Verifying,
            Some(detail("Cancelling login and checking the profile.")),
        );
    }
    Ok(())
}

#[tauri::command]
pub async fn verify_profile(
    manager: State<'_, ProfileManager>,
    profile_id: String,
) -> Result<Profile, String> {
    let spec = profile_spec(&profile_id)?;
    if manager.is_active(spec.id) {
        return Ok(manager.snapshot(spec));
    }
    tauri::async_runtime::spawn_blocking(move || probe_profile(spec))
        .await
        .map_err(|_| "could not verify the provider profile".to_string())
}

#[tauri::command]
pub async fn disconnect_profile(
    manager: State<'_, ProfileManager>,
    profile_id: String,
) -> Result<Profile, String> {
    let spec = profile_spec(&profile_id)?;
    if manager.is_active(spec.id) {
        return Err("cancel the login before disconnecting this profile".to_string());
    }
    if service_is_running(spec)? {
        return Err(
            "this provider worker is running; stop the stack before disconnecting it".to_string(),
        );
    }
    tauri::async_runtime::spawn_blocking(move || {
        if spec.provider == Provider::Claude {
            run_profile_command(
                spec,
                "/usr/local/bin/commitarium-claude-api-key",
                &["clear"],
                Stdio::null(),
                Stdio::null(),
                Stdio::null(),
            )?;
        }
        let arguments: &[&str] = match spec.provider {
            Provider::Codex => &["logout"],
            Provider::Claude => &["auth", "logout"],
        };
        // A provider may report "already logged out" as non-zero. The real
        // status probe below is authoritative, so no CLI text is surfaced.
        let _ = run_profile_command(
            spec,
            spec.executable,
            arguments,
            Stdio::null(),
            Stdio::null(),
            Stdio::null(),
        );
        let result = probe_profile(spec);
        match result.status {
            ProfileStatus::NotConfigured => Ok(profile(
                spec,
                ProfileStatus::NotConfigured,
                Some(detail("Profile disconnected.")),
            )),
            ProfileStatus::Connected => Err("the provider profile is still connected".to_string()),
            ProfileStatus::Expired => {
                Err("stored provider credentials could not be removed".to_string())
            }
            _ => Err("the disconnected provider profile could not be verified".to_string()),
        }
    })
    .await
    .map_err(|_| "could not disconnect the provider profile".to_string())?
}

fn start_subscription(
    app: AppHandle,
    manager: ProfileManager,
    spec: ProfileSpec,
) -> Result<(), String> {
    let generation = manager.reserve(spec, LoginMethod::Subscription, ProfileStatus::Starting)?;
    manager.transition(
        &app,
        spec,
        ProfileStatus::Starting,
        Some(detail("Starting provider login.")),
    );

    let arguments: &[&str] = match spec.provider {
        Provider::Codex => &[
            "-c",
            "cli_auth_credentials_store=\"file\"",
            "login",
            "--device-auth",
        ],
        Provider::Claude => &["auth", "login"],
    };
    let mut child = match spawn_profile_command(spec, spec.executable, arguments) {
        Ok(child) => child,
        Err(error) => {
            manager.finish(spec, generation);
            manager.transition(&app, spec, ProfileStatus::Failed, Some(detail(&error)));
            return Err(error);
        }
    };
    let stdin = Arc::new(Mutex::new(child.stdin.take()));
    let stdout = child.stdout.take();
    let stderr = child.stderr.take();
    let child = Arc::new(Mutex::new(child));
    let cancelled = manager.attach_process(spec, generation, child.clone(), stdin)?;

    thread::spawn(move || {
        monitor_subscription(
            app, manager, spec, generation, child, stdout, stderr, cancelled,
        )
    });
    Ok(())
}

fn provision_api_key(
    app: AppHandle,
    manager: ProfileManager,
    spec: ProfileSpec,
    generation: u64,
    key: Zeroizing<Vec<u8>>,
) {
    if manager
        .active(spec)
        .is_some_and(|active| active.cancelled.load(Ordering::Acquire))
    {
        let current = probe_profile(spec);
        manager.transition(
            &app,
            spec,
            current.status,
            Some(ProfileDetail {
                message: Some(
                    "Login cancelled; the current profile state was checked.".to_string(),
                ),
                ..current.detail.unwrap_or_default()
            }),
        );
        manager.finish(spec, generation);
        return;
    }
    manager.transition(
        &app,
        spec,
        ProfileStatus::Starting,
        Some(detail(
            "Saving the API key in this profile's private volume.",
        )),
    );
    let (executable, arguments): (&str, &[&str]) = match spec.provider {
        Provider::Codex => (
            "codex",
            &[
                "-c",
                "cli_auth_credentials_store=\"file\"",
                "login",
                "--with-api-key",
            ],
        ),
        Provider::Claude => ("/usr/local/bin/commitarium-claude-api-key", &["store"]),
    };
    let result = run_secret_command(&manager, spec, generation, executable, arguments, &key);
    if result.is_ok() {
        manager.transition(
            &app,
            spec,
            ProfileStatus::Verifying,
            Some(detail("Verifying the provider login.")),
        );
        let verified = probe_profile(spec);
        manager.transition(&app, spec, verified.status, verified.detail);
    } else {
        let cancelled = manager
            .active(spec)
            .is_some_and(|active| active.cancelled.load(Ordering::Acquire));
        if cancelled {
            let current = probe_profile(spec);
            manager.transition(
                &app,
                spec,
                current.status,
                Some(ProfileDetail {
                    message: Some(
                        "Login cancelled; the current profile state was checked.".to_string(),
                    ),
                    ..current.detail.unwrap_or_default()
                }),
            );
        } else {
            manager.transition(
                &app,
                spec,
                ProfileStatus::Failed,
                Some(detail("The provider could not accept this API key.")),
            );
        }
    }
    manager.finish(spec, generation);
}

fn run_secret_command(
    manager: &ProfileManager,
    spec: ProfileSpec,
    generation: u64,
    executable: &str,
    arguments: &[&str],
    secret: &[u8],
) -> Result<(), String> {
    let mut child = spawn_profile_command(spec, executable, arguments)?;
    let mut input = child
        .stdin
        .take()
        .ok_or_else(|| "provider login input is unavailable".to_string())?;
    let child = Arc::new(Mutex::new(child));
    let stdin = Arc::new(Mutex::new(None));
    let cancelled = match manager.attach_process(spec, generation, child.clone(), stdin) {
        Ok(cancelled) => cancelled,
        Err(error) => {
            let mut process = child.lock().expect("login process poisoned");
            let _ = process.kill();
            let _ = process.wait();
            return Err(error);
        }
    };
    if cancelled.load(Ordering::Acquire) {
        let mut process = child.lock().expect("login process poisoned");
        let _ = process.kill();
        let _ = process.wait();
        return Err("provider login cancelled".to_string());
    }
    if input.write_all(secret).is_err() {
        let _ = child.lock().expect("login process poisoned").kill();
        return Err("could not send the API key to the provider".to_string());
    }
    if spec.provider == Provider::Codex && input.write_all(b"\n").is_err() {
        let _ = child.lock().expect("login process poisoned").kill();
        return Err("could not send the API key to the provider".to_string());
    }
    if input.flush().is_err() {
        let _ = child.lock().expect("login process poisoned").kill();
        return Err("could not send the API key to the provider".to_string());
    }
    drop(input);
    loop {
        if cancelled.load(Ordering::Acquire) {
            let mut process = child.lock().expect("login process poisoned");
            let _ = process.kill();
            let _ = process.wait();
            return Err("provider login cancelled".to_string());
        }
        if let Some(status) = child
            .lock()
            .expect("login process poisoned")
            .try_wait()
            .map_err(|_| "could not wait for provider login".to_string())?
        {
            return if status.success() {
                Ok(())
            } else {
                Err("the provider rejected the login".to_string())
            };
        }
        thread::sleep(POLL_INTERVAL);
    }
}

#[allow(clippy::too_many_arguments)]
fn monitor_subscription(
    app: AppHandle,
    manager: ProfileManager,
    spec: ProfileSpec,
    generation: u64,
    child: Arc<Mutex<Child>>,
    stdout: Option<std::process::ChildStdout>,
    stderr: Option<std::process::ChildStderr>,
    cancelled: Arc<AtomicBool>,
) {
    let (sender, receiver) = mpsc::channel();
    if let Some(stdout) = stdout {
        spawn_reader(stdout, sender.clone());
    }
    if let Some(stderr) = stderr {
        spawn_reader(stderr, sender.clone());
    }
    drop(sender);

    let mut parser = LoginOutputParser::new(spec.provider);
    let exit_status = loop {
        while let Ok(bytes) = receiver.try_recv() {
            parser.ingest(&bytes);
            publish_parser_progress(&app, &manager, spec, &mut parser);
        }
        if cancelled.load(Ordering::Acquire) {
            let mut process = child.lock().expect("login process poisoned");
            let _ = process.kill();
            let _ = process.wait();
            break None;
        }
        match child.lock().expect("login process poisoned").try_wait() {
            Ok(Some(status)) => break Some(status),
            Ok(None) => thread::sleep(POLL_INTERVAL),
            Err(_) => break None,
        }
    };
    while let Ok(bytes) = receiver.try_recv() {
        parser.ingest(&bytes);
        publish_parser_progress(&app, &manager, spec, &mut parser);
    }

    if cancelled.load(Ordering::Acquire) {
        let current = probe_profile(spec);
        manager.transition(
            &app,
            spec,
            current.status,
            Some(ProfileDetail {
                message: Some(
                    "Login cancelled; the current profile state was checked.".to_string(),
                ),
                ..current.detail.unwrap_or_default()
            }),
        );
    } else {
        finish_subscription(&app, &manager, spec, exit_status);
    }
    manager.finish(spec, generation);
}

fn finish_subscription(
    app: &AppHandle,
    manager: &ProfileManager,
    spec: ProfileSpec,
    exit_status: Option<ExitStatus>,
) {
    if exit_status.is_some_and(|status| status.success()) {
        manager.transition(
            app,
            spec,
            ProfileStatus::Verifying,
            Some(detail("Verifying the provider login.")),
        );
        let verified = probe_profile(spec);
        manager.transition(app, spec, verified.status, verified.detail);
    } else {
        manager.transition(
            app,
            spec,
            ProfileStatus::Failed,
            Some(detail("Provider login did not complete.")),
        );
    }
}

fn spawn_reader(mut reader: impl Read + Send + 'static, sender: mpsc::Sender<Zeroizing<Vec<u8>>>) {
    thread::spawn(move || {
        let mut buffer = [0_u8; 2048];
        loop {
            match reader.read(&mut buffer) {
                Ok(0) | Err(_) => return,
                Ok(count) => {
                    if sender
                        .send(Zeroizing::new(buffer[..count].to_vec()))
                        .is_err()
                    {
                        return;
                    }
                }
            }
        }
    });
}

fn publish_parser_progress(
    app: &AppHandle,
    manager: &ProfileManager,
    spec: ProfileSpec,
    parser: &mut LoginOutputParser,
) {
    if parser.url_changed {
        parser.url_changed = false;
        manager.transition(
            app,
            spec,
            ProfileStatus::WaitingForBrowser,
            Some(ProfileDetail {
                message: Some("Open the provider login page in your browser.".to_string()),
                browser_url: parser.browser_url.clone(),
                device_code: parser.device_code.clone(),
            }),
        );
    }
    if parser.code_changed || parser.waiting_for_paste_changed {
        parser.code_changed = false;
        parser.waiting_for_paste_changed = false;
        manager.transition(
            app,
            spec,
            ProfileStatus::WaitingForCode,
            Some(ProfileDetail {
                message: Some(match spec.provider {
                    Provider::Codex => {
                        "Enter this one-time code on the provider login page.".to_string()
                    }
                    Provider::Claude => {
                        "Complete browser login, then paste the returned code here.".to_string()
                    }
                }),
                browser_url: parser.browser_url.clone(),
                device_code: parser.device_code.clone(),
            }),
        );
    }
}

struct LoginOutputParser {
    provider: Provider,
    output: Zeroizing<String>,
    browser_url: Option<String>,
    device_code: Option<String>,
    waiting_for_paste: bool,
    url_changed: bool,
    code_changed: bool,
    waiting_for_paste_changed: bool,
}

impl LoginOutputParser {
    fn new(provider: Provider) -> Self {
        Self {
            provider,
            output: Zeroizing::new(String::new()),
            browser_url: None,
            device_code: None,
            waiting_for_paste: false,
            url_changed: false,
            code_changed: false,
            waiting_for_paste_changed: false,
        }
    }

    fn ingest(&mut self, bytes: &[u8]) {
        self.output.push_str(&String::from_utf8_lossy(bytes));
        if self.output.len() > OUTPUT_LIMIT {
            let mut keep_from = self.output.len() - OUTPUT_LIMIT;
            while !self.output.is_char_boundary(keep_from) {
                keep_from += 1;
            }
            self.output.drain(..keep_from);
        }
        if self.browser_url.is_none() {
            if let Some(url) = extract_trusted_url(&self.output, self.provider) {
                self.browser_url = Some(url);
                self.url_changed = true;
            }
        }
        if self.provider == Provider::Codex && self.device_code.is_none() {
            if let Some(code) = extract_device_code(&self.output) {
                self.device_code = Some(code);
                self.code_changed = true;
            }
        }
        if self.provider == Provider::Claude
            && !self.waiting_for_paste
            && self.output.to_ascii_lowercase().contains("paste code here")
        {
            self.waiting_for_paste = true;
            self.waiting_for_paste_changed = true;
        }
    }
}

fn extract_trusted_url(output: &str, provider: Provider) -> Option<String> {
    for (start, _) in output.match_indices("https://") {
        let tail = &output[start..];
        let end = tail
            .char_indices()
            .find_map(|(index, character)| {
                (index > 0
                    && (character.is_ascii_whitespace()
                        || character.is_ascii_control()
                        || matches!(character, '\x1b' | '"' | '\'' | '<' | '>')))
                .then_some(index)
            })
            .unwrap_or(tail.len());
        let candidate = &tail[..end];
        let trusted = match provider {
            Provider::Codex => candidate.starts_with("https://auth.openai.com/"),
            Provider::Claude => {
                candidate.starts_with("https://claude.com/")
                    || candidate.starts_with("https://platform.claude.com/")
            }
        };
        if trusted {
            return Some(candidate.to_string());
        }
    }
    None
}

fn extract_device_code(output: &str) -> Option<String> {
    output
        .split(|character: char| !(character.is_ascii_alphanumeric() || character == '-'))
        .find(|token| {
            let mut parts = token.split('-');
            let left = parts.next().unwrap_or_default();
            let right = parts.next().unwrap_or_default();
            parts.next().is_none()
                && left.len() == 4
                && right.len() == 5
                && token
                    .bytes()
                    .filter(|byte| *byte != b'-')
                    .all(|byte| byte.is_ascii_uppercase() || byte.is_ascii_digit())
        })
        .map(str::to_string)
}

fn detail(message: &str) -> ProfileDetail {
    ProfileDetail {
        message: Some(message.to_string()),
        browser_url: None,
        device_code: None,
    }
}

fn probe_profile(spec: ProfileSpec) -> Profile {
    let arguments: &[&str] = match spec.provider {
        Provider::Codex => &[
            "-c",
            "cli_auth_credentials_store=\"file\"",
            "login",
            "status",
        ],
        Provider::Claude => &["auth", "status"],
    };
    match profile_command_status(
        spec,
        spec.executable,
        arguments,
        Stdio::null(),
        Stdio::null(),
        Stdio::null(),
    ) {
        Ok(true) => profile(
            spec,
            ProfileStatus::Connected,
            Some(detail("Provider login verified.")),
        ),
        Ok(false) => match profile_has_credentials(spec) {
            Ok(true) => profile(
                spec,
                ProfileStatus::Expired,
                Some(detail("Stored provider login is no longer valid.")),
            ),
            Ok(false) => profile(spec, ProfileStatus::NotConfigured, None),
            Err(_) => profile(
                spec,
                ProfileStatus::Failed,
                Some(detail("The provider profile could not be inspected.")),
            ),
        },
        Err(_) => profile(
            spec,
            ProfileStatus::Failed,
            Some(detail("The provider profile could not be inspected.")),
        ),
    }
}

fn profile_has_credentials(spec: ProfileSpec) -> Result<bool, String> {
    let script = match spec.provider {
        Provider::Codex => "test -s /var/lib/commitarium-provider/auth.json",
        Provider::Claude => {
            "test -s /var/lib/commitarium-provider/.credentials.json || test -s /var/lib/commitarium-provider/commitarium-api-key"
        }
    };
    profile_command_status(
        spec,
        "/bin/sh",
        &["-c", script],
        Stdio::null(),
        Stdio::null(),
        Stdio::null(),
    )
}

fn service_is_running(spec: ProfileSpec) -> Result<bool, String> {
    let compose_file = docker::compose_file()?;
    let output = Command::new("docker")
        .args(["compose", "--profile", spec.compose_profile, "-f"])
        .arg(compose_file)
        .args([
            "-p",
            docker::PROJECT_NAME,
            "ps",
            "--status",
            "running",
            "--services",
            spec.service,
        ])
        .output()
        .map_err(|_| "could not inspect the provider worker".to_string())?;
    if !output.status.success() {
        return Err("could not inspect the provider worker".to_string());
    }
    Ok(String::from_utf8_lossy(&output.stdout)
        .lines()
        .any(|line| line.trim() == spec.service))
}

fn compose_command(
    spec: ProfileSpec,
    executable: &str,
    running_service: bool,
    login_container: Option<&str>,
) -> Result<Command, String> {
    let compose_file = docker::compose_file()?;
    let mut command = Command::new("docker");
    command
        .args(["compose", "--profile", spec.compose_profile, "-f"])
        .arg(compose_file)
        .args(["-p", docker::PROJECT_NAME]);
    if running_service {
        command.args(["exec", "-T", spec.service, executable]);
    } else {
        command.args(["run", "--rm", "--no-deps"]);
        if let Some(container) = login_container {
            command.args(["--name", container]);
        }
        command.args(["--entrypoint", executable, spec.service]);
    }
    Ok(command)
}

fn spawn_profile_command(
    spec: ProfileSpec,
    executable: &str,
    arguments: &[&str],
) -> Result<Child, String> {
    // A process left by an unclean desktop exit has a fixed internal name.
    // Starting a new user-requested login removes only that exact stale helper,
    // never a worker or arbitrary container.
    remove_login_container(spec);
    let mut command = compose_command(spec, executable, false, Some(spec.login_container))?;
    command
        .args(arguments)
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .map_err(|_| "could not start the provider login container".to_string())
}

fn run_profile_command(
    spec: ProfileSpec,
    executable: &str,
    arguments: &[&str],
    stdin: Stdio,
    stdout: Stdio,
    stderr: Stdio,
) -> Result<(), String> {
    match profile_command_status(spec, executable, arguments, stdin, stdout, stderr)? {
        true => Ok(()),
        false => Err("provider profile command failed".to_string()),
    }
}

fn profile_command_status(
    spec: ProfileSpec,
    executable: &str,
    arguments: &[&str],
    stdin: Stdio,
    stdout: Stdio,
    stderr: Stdio,
) -> Result<bool, String> {
    let running_service = service_is_running(spec)?;
    let status = compose_command(spec, executable, running_service, None)?
        .args(arguments)
        .stdin(stdin)
        .stdout(stdout)
        .stderr(stderr)
        .status()
        .map_err(|_| "could not run the provider profile command".to_string())?;
    Ok(status.success())
}

fn remove_login_container(spec: ProfileSpec) {
    let _ = Command::new("docker")
        .args(["rm", "--force", spec.login_container])
        .stdin(Stdio::null())
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status();
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn profile_ids_map_to_separate_provider_and_role_services() {
        assert_eq!(PROFILES.len(), 4);
        assert_eq!(profile_spec("codex-lead").unwrap().service, "codex-worker");
        assert_eq!(
            profile_spec("claude-reviewer").unwrap().service,
            "claude-reviewer-worker"
        );
        assert!(profile_spec("codex").is_err());
        assert_eq!(
            paired_spec(profile_spec("claude-lead").unwrap()).id,
            "claude-reviewer"
        );
    }

    #[test]
    fn login_method_is_a_closed_contract() {
        assert_eq!(
            LoginMethod::parse("subscription").unwrap(),
            LoginMethod::Subscription
        );
        assert_eq!(LoginMethod::parse("api_key").unwrap(), LoginMethod::ApiKey);
        assert!(LoginMethod::parse("token").is_err());
    }

    #[test]
    fn codex_output_exposes_only_the_trusted_url_and_device_code() {
        let mut parser = LoginOutputParser::new(Provider::Codex);
        parser.ingest(
            b"Open https://evil.example/x then https://auth.openai.com/codex/device\ncode 80HQ-J9E0B\nsecret-token",
        );
        assert_eq!(
            parser.browser_url.as_deref(),
            Some("https://auth.openai.com/codex/device")
        );
        assert_eq!(parser.device_code.as_deref(), Some("80HQ-J9E0B"));
        assert!(!parser.browser_url.unwrap().contains("secret-token"));
    }

    #[test]
    fn claude_output_handles_terminal_hyperlinks_and_paste_prompt() {
        let mut parser = LoginOutputParser::new(Provider::Claude);
        parser.ingest(
            b"\x1b]8;;https://claude.com/cai/oauth/authorize?code=true\x07Login\x1b]8;;\x07\nPaste code here if prompted >",
        );
        assert_eq!(
            parser.browser_url.as_deref(),
            Some("https://claude.com/cai/oauth/authorize?code=true")
        );
        assert!(parser.waiting_for_paste_changed);
        parser.waiting_for_paste_changed = false;
        parser.ingest(b"more output");
        assert!(!parser.waiting_for_paste_changed);
    }

    #[test]
    fn unrelated_codes_and_urls_are_not_exposed() {
        assert_eq!(extract_device_code("short ABC-123"), None);
        assert_eq!(
            extract_trusted_url("https://example.com/callback", Provider::Claude),
            None
        );
    }

    #[test]
    fn profile_manager_fences_duplicate_logins() {
        let manager = ProfileManager::new();
        let spec = profile_spec("codex-lead").unwrap();
        manager
            .reserve(spec, LoginMethod::ApiKey, ProfileStatus::WaitingForApiKey)
            .unwrap();
        assert!(manager
            .reserve(spec, LoginMethod::Subscription, ProfileStatus::Starting)
            .is_err());
    }

    #[test]
    fn profile_serialization_matches_the_frontend_contract() {
        let value = serde_json::to_value(profile(
            profile_spec("codex-lead").unwrap(),
            ProfileStatus::WaitingForBrowser,
            Some(ProfileDetail {
                message: Some("Open the page.".to_string()),
                browser_url: Some("https://auth.openai.com/codex/device".to_string()),
                device_code: Some("ABCD-12345".to_string()),
            }),
        ))
        .unwrap();
        assert_eq!(value["id"], "codex-lead");
        assert_eq!(value["provider"], "codex");
        assert_eq!(value["role"], "lead");
        assert_eq!(value["status"], "waiting_for_browser");
        assert_eq!(
            value["detail"]["browserUrl"],
            "https://auth.openai.com/codex/device"
        );
        assert_eq!(value["detail"]["deviceCode"], "ABCD-12345");
    }

    #[test]
    fn compose_command_contains_only_fixed_non_secret_arguments() {
        let root = tempfile::tempdir().unwrap();
        let compose_file = root.path().join("compose.yml");
        std::fs::write(&compose_file, "services: {}").unwrap();
        std::env::set_var("COMMITARIUM_COMPOSE_FILE", &compose_file);
        let command = compose_command(
            profile_spec("claude-lead").unwrap(),
            "claude",
            false,
            Some("commitarium-login-claude-lead"),
        )
        .unwrap();
        let arguments: Vec<_> = command
            .get_args()
            .map(|argument| argument.to_string_lossy().into_owned())
            .collect();
        assert!(arguments
            .windows(2)
            .any(|pair| pair == ["--entrypoint", "claude"]));
        assert!(arguments
            .windows(2)
            .any(|pair| { pair == ["--name", "commitarium-login-claude-lead"] }));
        assert_eq!(arguments.last().map(String::as_str), Some("claude-worker"));
        std::env::remove_var("COMMITARIUM_COMPOSE_FILE");
    }
}
