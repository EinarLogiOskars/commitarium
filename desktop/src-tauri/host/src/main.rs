//! `commitarium-host`: started by the desktop app, which gives it a token over
//! stdin and keeps that pipe open. When the pipe closes, the app is gone, so
//! the host leaves too and no old host outlives an app or blocks an update.

use std::io::{self, BufRead, Write};
use std::thread;

use commitarium_host::server::{self, Access, Launch, Ready};

fn main() {
    if let Err(error) = run() {
        eprintln!("[host] {error}");
        std::process::exit(1);
    }
}

fn run() -> Result<(), String> {
    let mut stdin = io::stdin().lock();
    let mut line = String::new();
    stdin
        .read_line(&mut line)
        .map_err(|e| format!("read launch: {e}"))?;
    let launch: Launch = serde_json::from_str(&line).map_err(|e| format!("read launch: {e}"))?;
    if launch.token.is_empty() {
        return Err("launch has no token".into());
    }

    let http = tiny_http::Server::http("127.0.0.1:0").map_err(|e| format!("listen: {e}"))?;
    let port = http
        .server_addr()
        .to_ip()
        .ok_or("the server has no IP address")?
        .port();
    let access = Access::new(launch, port);
    thread::spawn(move || server::serve(http, access));

    let ready = Ready {
        port,
        version: env!("CARGO_PKG_VERSION").to_string(),
    };
    let ready = serde_json::to_string(&ready).map_err(|e| e.to_string())?;
    let mut stdout = io::stdout();
    writeln!(stdout, "{ready}")
        .and_then(|()| stdout.flush())
        .map_err(|e| format!("announce: {e}"))?;

    // Blocks until the app closes the pipe, whichever way it went.
    let mut rest = Vec::new();
    while stdin
        .read_until(b'\n', &mut rest)
        .is_ok_and(|read| read > 0)
    {
        rest.clear();
    }
    Ok(())
}
