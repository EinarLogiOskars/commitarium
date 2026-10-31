//! The real binary, the way the desktop app runs it.

use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpStream;
use std::process::{Child, ChildStdin, Command, Stdio};
use std::time::{Duration, Instant};

use commitarium_host::server::{Launch, Ready};

fn start() -> (Child, ChildStdin, Ready) {
    let mut child = Command::new(env!("CARGO_BIN_EXE_commitarium-host"))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .spawn()
        .unwrap();
    let mut stdin = child.stdin.take().unwrap();
    let launch = Launch {
        token: "launch-token".into(),
        origins: vec!["tauri://localhost".into()],
        data_dir: std::env::temp_dir(),
    };
    writeln!(stdin, "{}", serde_json::to_string(&launch).unwrap()).unwrap();
    let mut line = String::new();
    BufReader::new(child.stdout.take().unwrap())
        .read_line(&mut line)
        .unwrap();
    let ready = serde_json::from_str(&line).unwrap();
    (child, stdin, ready)
}

fn get(port: u16, path: &str, headers: &str) -> String {
    let mut stream = TcpStream::connect(("127.0.0.1", port)).unwrap();
    write!(
        stream,
        "GET {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n{headers}\r\n"
    )
    .unwrap();
    let mut response = String::new();
    stream.read_to_string(&mut response).unwrap();
    response
}

#[test]
fn answers_only_with_its_token_and_leaves_with_the_app() {
    let (mut child, stdin, ready) = start();
    assert_eq!(ready.version, env!("CARGO_PKG_VERSION"));

    let bearer = "Authorization: Bearer launch-token\r\n";
    assert!(get(ready.port, "/v1/version", "").starts_with("HTTP/1.1 401"));
    assert!(get(
        ready.port,
        "/v1/version",
        &format!("Origin: http://evil\r\n{bearer}")
    )
    .starts_with("HTTP/1.1 401"));
    let answer = get(ready.port, "/v1/version", bearer);
    assert!(answer.starts_with("HTTP/1.1 200"), "{answer}");
    assert!(answer.contains(env!("CARGO_PKG_VERSION")));
    assert!(!answer.to_lowercase().contains("access-control"));
    assert!(get(ready.port, "/v1/nothing", bearer).starts_with("HTTP/1.1 404"));

    drop(stdin);
    let deadline = Instant::now() + Duration::from_secs(5);
    while child.try_wait().unwrap().is_none() {
        assert!(Instant::now() < deadline, "the host outlived its app");
        std::thread::sleep(Duration::from_millis(20));
    }
}
