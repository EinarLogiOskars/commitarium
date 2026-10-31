//! The event stream over a real socket.

use std::io::{Read, Write};
use std::net::TcpStream;
use std::sync::Arc;
use std::thread;
use std::time::Duration;

use commitarium_host::events::{Bus, Frames};
use commitarium_host::server::{self, Access, Launch, Shared};

fn serve(events: Bus) -> u16 {
    let http = tiny_http::Server::http("127.0.0.1:0").unwrap();
    let port = http.server_addr().to_ip().unwrap().port();
    let launch = Launch {
        token: "launch-token".into(),
        origins: vec![],
        data_dir: std::env::temp_dir(),
    };
    let access = Access::new(launch, port);
    // This never begins the host's background work, so no Docker is touched.
    let shared = Arc::new(Shared::new(std::env::temp_dir(), events));
    thread::spawn(move || server::serve(http, access, shared));
    port
}

fn connect(port: u16, headers: &str) -> TcpStream {
    let mut stream = TcpStream::connect(("127.0.0.1", port)).unwrap();
    stream
        .set_read_timeout(Some(Duration::from_secs(5)))
        .unwrap();
    write!(
        stream,
        "GET /v1/events HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\n{headers}\r\n"
    )
    .unwrap();
    stream
}

/// Reads until `done` holds for what has arrived.
fn read_until(stream: &mut TcpStream, done: impl Fn(&str) -> bool) -> String {
    let mut seen = Vec::new();
    let mut piece = [0; 1024];
    while !done(&String::from_utf8_lossy(&seen)) {
        let read = stream.read(&mut piece).unwrap();
        assert!(read > 0, "the stream ended early");
        seen.extend_from_slice(&piece[..read]);
    }
    String::from_utf8_lossy(&seen).into_owned()
}

#[test]
fn updates_arrive_at_once_and_only_with_the_token() {
    let events = Bus::default();
    let port = serve(events.clone());

    let mut stranger = connect(port, "Connection: close\r\n");
    assert!(read_until(&mut stranger, |seen| seen.contains("\r\n")).starts_with("HTTP/1.1 401"));

    let mut client = connect(port, "Authorization: Bearer launch-token\r\n");
    let start = read_until(&mut client, |seen| seen.contains(": connected\n\n"));
    assert!(start.starts_with("HTTP/1.1 200"), "{start}");
    assert!(start.contains("text/event-stream"));

    // The stream stays open, so this arrives only if it is sent straight away.
    events.publish("status", &vec!["ok"]);
    let update = read_until(&mut client, |seen| seen.ends_with("\n\n"));
    assert_eq!(
        Frames::default().push(update.as_bytes()),
        vec![("status".to_string(), "[\"ok\"]".to_string())]
    );
}
