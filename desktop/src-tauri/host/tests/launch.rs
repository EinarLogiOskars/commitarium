//! The real binary, the way the desktop app runs it.
//!
//! The host sweeps preview leftovers and starts the CLI updater as soon as it
//! starts, and those act on whatever Docker and temp directory it finds. So
//! every test here runs it with a clean environment: a fake `docker` first on
//! PATH that only records its arguments, its own temp directory, and a
//! coordinator of the test's own. A test must never start it any other way.

#![cfg(unix)]

use std::fs;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpStream;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;
use std::process::{Child, ChildStdin, Command, Stdio};
use std::thread;
use std::time::{Duration, Instant};

use commitarium_host::server::{Launch, Ready, MAX_BODY};
use tempfile::TempDir;

const BEARER: &str = "Authorization: Bearer launch-token\r\n";

/// Starts a stand-in coordinator that lists `agents` as JSON.
fn coordinator(agents: &'static str) -> u16 {
    let server = tiny_http::Server::http("127.0.0.1:0").unwrap();
    let port = server.server_addr().to_ip().unwrap().port();
    thread::spawn(move || {
        for request in server.incoming_requests() {
            let _ = request.respond(tiny_http::Response::from_string(agents));
        }
    });
    port
}

struct Sandbox {
    root: TempDir,
    calls: PathBuf,
    child: Child,
    stdin: Option<ChildStdin>,
    port: u16,
    version: String,
}

impl Sandbox {
    fn start() -> Self {
        Self::with_agents("[]")
    }

    fn with_agents(agents: &'static str) -> Self {
        let root = tempfile::tempdir().unwrap();
        let path = root.path();
        let bin = path.join("bin");
        fs::create_dir(&bin).unwrap();
        let docker = bin.join("docker");
        fs::write(
            &docker,
            "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$FAKE_DOCKER_CALLS\"\n",
        )
        .unwrap();
        fs::set_permissions(&docker, fs::Permissions::from_mode(0o755)).unwrap();
        let temp = path.join("tmp");
        fs::create_dir(&temp).unwrap();
        let compose = path.join("compose.yml");
        fs::write(&compose, "services: {}\n").unwrap();
        let calls = path.join("docker-calls");
        // With these in place a login needs nothing from Forgejo or Compose.
        fs::write(
            path.join("compose.agents.yml"),
            r#"{"services":{"agent-codex-worker":{}}}"#,
        )
        .unwrap();
        for role in ["lead", "reviewer"] {
            let agent = path.join(format!(".commitarium/agents/codex-{role}"));
            fs::create_dir_all(&agent).unwrap();
            fs::write(agent.join("forgejo-token"), "token").unwrap();
        }

        // Nothing of this machine's environment reaches the host.
        let mut child = Command::new(env!("CARGO_BIN_EXE_commitarium-host"))
            .env_clear()
            .env("PATH", format!("{}:/usr/bin:/bin", bin.display()))
            .env("HOME", path)
            .env("TMPDIR", &temp)
            .env("FAKE_DOCKER_CALLS", &calls)
            // A closed port, so a running stack's jobs are left alone.
            .env("COMMITARIUM_COORDINATOR_PORT", "1")
            .env("COMMITARIUM_COMPOSE_FILE", &compose)
            .env(
                "COMMITARIUM_AGENTS_COMPOSE_FILE",
                path.join("compose.agents.yml"),
            )
            .env(
                "COMMITARIUM_ENVIRONMENT_COMPOSE_FILE",
                path.join("compose.environment.yml"),
            )
            .env(
                "COMMITARIUM_COORDINATOR_PORT",
                coordinator(agents).to_string(),
            )
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .spawn()
            .unwrap();
        let mut stdin = child.stdin.take().unwrap();
        let launch = Launch {
            token: "launch-token".into(),
            origins: vec!["tauri://localhost".into()],
            data_dir: path.join("data"),
        };
        writeln!(stdin, "{}", serde_json::to_string(&launch).unwrap()).unwrap();
        let mut line = String::new();
        BufReader::new(child.stdout.take().unwrap())
            .read_line(&mut line)
            .unwrap();
        let ready: Ready = serde_json::from_str(&line).unwrap();
        Self {
            root,
            calls,
            child,
            stdin: Some(stdin),
            port: ready.port,
            version: ready.version,
        }
    }

    fn get(&self, path: &str, headers: &str) -> String {
        self.send(&format!("GET {path} HTTP/1.1"), headers, b"")
    }

    fn post(&self, path: &str, headers: &str, body: &str) -> String {
        let headers = format!("{headers}Content-Length: {}\r\n", body.len());
        self.send(&format!("POST {path} HTTP/1.1"), &headers, body.as_bytes())
    }

    fn send(&self, start: &str, headers: &str, body: &[u8]) -> String {
        let port = self.port;
        let mut stream = TcpStream::connect(("127.0.0.1", port)).unwrap();
        stream
            .set_read_timeout(Some(Duration::from_secs(20)))
            .unwrap();
        write!(
            stream,
            "{start}\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n{headers}\r\n"
        )
        .unwrap();
        // The host may answer before taking the whole of a refused body.
        let _ = stream.write_all(body);
        let mut response = Vec::new();
        let _ = stream.read_to_end(&mut response);
        String::from_utf8_lossy(&response).into_owned()
    }

    fn docker_calls(&self) -> String {
        fs::read_to_string(&self.calls).unwrap_or_default()
    }

    /// Waits for `seen` to hold of the calls to Docker so far.
    fn wait_for_docker(&self, seen: impl Fn(&str) -> bool) -> String {
        let deadline = Instant::now() + Duration::from_secs(20);
        loop {
            let calls = self.docker_calls();
            if seen(&calls) {
                return calls;
            }
            assert!(Instant::now() < deadline, "docker saw only:\n{calls}");
            thread::sleep(Duration::from_millis(20));
        }
    }

    /// Closes the app's end of the pipe, as a crash of the app would.
    fn close_pipe(&mut self) {
        drop(self.stdin.take());
        let deadline = Instant::now() + Duration::from_secs(20);
        while self.child.try_wait().unwrap().is_none() {
            assert!(Instant::now() < deadline, "the host outlived its app");
            thread::sleep(Duration::from_millis(20));
        }
    }
}

impl Drop for Sandbox {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

fn status(response: &str) -> &str {
    response.split(' ').nth(1).unwrap_or(response)
}

fn body(response: &str) -> &str {
    response.split("\r\n\r\n").nth(1).unwrap_or("")
}

/// The sweep at startup is the first thing the host asks Docker. Seeing it in
/// the fake's record proves the fake is the Docker this host runs.
#[test]
fn the_host_only_ever_finds_the_fake_docker() {
    let host = Sandbox::start();
    let calls = host.wait_for_docker(|calls| calls.contains("ps -a --filter label="));
    assert!(calls.starts_with("ps -a"), "{calls}");
    assert!(host.root.path().join("tmp").is_dir());
}

#[test]
fn answers_only_with_its_token_and_leaves_with_the_app() {
    let mut host = Sandbox::start();
    assert_eq!(host.version, env!("CARGO_PKG_VERSION"));

    assert_eq!(status(&host.get("/v1/version", "")), "401");
    let evil = format!("Origin: http://evil\r\n{BEARER}");
    assert_eq!(status(&host.get("/v1/version", &evil)), "401");
    let answer = host.get("/v1/version", BEARER);
    assert_eq!(status(&answer), "200", "{answer}");
    assert!(answer.contains(env!("CARGO_PKG_VERSION")));
    assert!(!answer.to_lowercase().contains("access-control"));
    assert_eq!(status(&host.get("/v1/nothing", BEARER)), "404");

    host.close_pipe();
}

/// A refused route that ran anyway would show up in the fake's record.
#[test]
fn no_work_route_runs_without_the_token() {
    let host = Sandbox::start();
    host.wait_for_docker(|calls| calls.contains("ps -a"));
    let routes = [
        "/v1/stack/up",
        "/v1/stack/down",
        "/v1/stack/update",
        "/v1/profiles/login/begin",
        "/v1/profiles/login/code",
        "/v1/profiles/login/key",
        "/v1/profiles/login/cancel",
        "/v1/profiles/verify",
        "/v1/profiles/disconnect",
        "/v1/profiles/shutdown",
        "/v1/previews/start",
        "/v1/previews/stop",
        "/v1/previews/reset",
        "/v1/previews/prune",
        "/v1/previews/status",
        "/v1/previews/logs",
        "/v1/previews/delete-project",
        "/v1/previews/stop-all",
        "/v1/environment/provision",
        "/v1/validation/run",
        "/v1/preview-check/run",
        "/v1/forgejo/viewer",
    ];
    let evil = format!("Origin: http://evil\r\n{BEARER}");
    for route in routes {
        assert_eq!(status(&host.post(route, "", "{}")), "401", "{route}");
        let wrong = "Authorization: Bearer wrong-token!\r\n";
        assert_eq!(status(&host.post(route, wrong, "{}")), "401", "{route}");
        assert_eq!(status(&host.post(route, &evil, "{}")), "401", "{route}");
    }
    for route in [
        "/v1/profiles",
        "/v1/agents/cli-status",
        "/v1/forgejo/viewer",
    ] {
        assert_eq!(status(&host.get(route, "")), "401", "{route}");
    }
    let calls = host.docker_calls();
    for work in [" up ", " down", " pull", "provision", " rm "] {
        assert!(!calls.contains(work), "{calls}");
    }
}

#[test]
fn a_body_over_the_limit_is_refused_before_any_work() {
    let host = Sandbox::start();
    let claimed = format!("{BEARER}Content-Length: {}\r\n", MAX_BODY + 1);
    let answer = host.send("POST /v1/previews/status HTTP/1.1", &claimed, b"");
    assert_eq!(status(&answer), "413", "{answer}");

    // A chunked body claims no length, so this one is counted as it is read.
    let size = MAX_BODY + 16;
    let mut chunked = format!("{size:x}\r\n").into_bytes();
    chunked.extend(std::iter::repeat_n(b' ', size));
    chunked.extend_from_slice(b"\r\n0\r\n\r\n");
    let headers = format!("{BEARER}Transfer-Encoding: chunked\r\n");
    let answer = host.send("POST /v1/previews/status HTTP/1.1", &headers, &chunked);
    assert_eq!(status(&answer), "413", "{answer}");

    // Up to the limit is read: spaces around a real body are valid JSON.
    let mut fits = " ".repeat(MAX_BODY - 40);
    fits.push_str(r#"{"project_id":"project-1"}"#);
    let answer = host.post("/v1/previews/status", BEARER, &fits);
    assert_eq!(status(&answer), "200", "{answer}");
}

#[test]
fn a_bad_body_is_refused_without_repeating_it() {
    let host = Sandbox::start();
    let answer = host.post(
        "/v1/profiles/login/key",
        BEARER,
        r#"{"profile_id":"a","key":12,"note":"sk-should-not-come-back"#,
    );
    assert_eq!(status(&answer), "400", "{answer}");
    assert!(!answer.contains("sk-should-not-come-back"));
    assert_eq!(
        status(&host.send("PUT /v1/stack/up HTTP/1.1", BEARER, b"")),
        "405"
    );
}

#[test]
fn the_works_own_error_comes_back_exactly() {
    let host = Sandbox::start();
    let answer = host.post("/v1/profiles/verify", BEARER, r#"{"profile_id":"nobody"}"#);
    assert_eq!(status(&answer), "500", "{answer}");
    assert_eq!(body(&answer), r#"{"error":"unknown agent"}"#);
}

#[test]
fn reads_answer_with_the_hosts_own_state() {
    let host = Sandbox::start();
    let answer = host.get("/v1/agents/cli-status", BEARER);
    assert_eq!(body(&answer), "[]", "{answer}");
    let asked = r#"{"project_id":"project-1","feature_id":null}"#;
    let answer = host.post("/v1/previews/status", BEARER, asked);
    assert_eq!(body(&answer), "null", "{answer}");
}

/// A login begun by one request is known to the next, and its update reaches
/// the event stream under its usual name. Closing the pipe then ends it.
#[test]
fn a_login_lives_in_the_host_and_ends_when_the_app_is_gone() {
    let mut host = Sandbox::with_agents(r#"[{"id":"codex","name":"Codex","provider":"codex"}]"#);
    host.wait_for_docker(|calls| calls.contains("ps -a"));

    let mut events = TcpStream::connect(("127.0.0.1", host.port)).unwrap();
    events
        .set_read_timeout(Some(Duration::from_secs(20)))
        .unwrap();
    write!(
        events,
        "GET /v1/events HTTP/1.1\r\nHost: 127.0.0.1:{}\r\n{BEARER}\r\n",
        host.port
    )
    .unwrap();
    let mut piece = [0; 4096];
    assert!(events.read(&mut piece).unwrap() > 0);

    let begin = r#"{"profile_id":"codex","method":"api_key"}"#;
    let answer = host.post("/v1/profiles/login/begin", BEARER, begin);
    assert_eq!(status(&answer), "200", "{answer}");
    assert_eq!(body(&answer), "null");

    let mut seen = String::new();
    while !seen.ends_with("\n\n") {
        let read = events.read(&mut piece).unwrap();
        seen.push_str(&String::from_utf8_lossy(&piece[..read]));
    }
    assert!(seen.contains("event: login_progress\n"), "{seen}");
    assert!(seen.contains(r#""profileId":"codex""#), "{seen}");
    assert!(seen.contains(r#""status":"waiting_for_api_key""#), "{seen}");

    let answer = host.post("/v1/profiles/login/begin", BEARER, begin);
    assert_eq!(
        body(&answer),
        r#"{"error":"a login is already in progress for this agent"}"#
    );

    host.close_pipe();
    let calls = host.docker_calls();
    assert!(
        calls.contains("rm --force commitarium-login-codex"),
        "{calls}"
    );
    // Previews stop first, which ends with a sweep of its own.
    assert!(
        calls.matches("ps -a --filter label=").count() >= 2,
        "{calls}"
    );
    assert!(!calls.contains(" down"), "the stack must stay up: {calls}");
}
