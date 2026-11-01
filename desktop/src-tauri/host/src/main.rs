//! `commitarium-host`: started by the desktop app, which gives it a token over
//! stdin and keeps that pipe open. When the pipe closes, the app is gone, so
//! the host leaves too and no old host outlives an app or blocks an update.
//! An app leaving normally has already stopped everything through the API and
//! kills the host; a pipe that just closes means it crashed, so the host stops
//! the previews and logins nobody could reach again. The stack stays up.
//!
//! On a server it runs with no app: `commitarium-host serve` is the service,
//! and `commitarium-host status` asks it how it is (`service.rs`).

use std::io::{self, BufRead, Write};
use std::sync::Arc;
use std::thread;

use commitarium_host::events::Bus;
use commitarium_host::jobs;
use commitarium_host::server::{self, Access, Launch, Ready, Shared};

fn main() {
    if let Err(error) = run() {
        eprintln!("[host] {error}");
        std::process::exit(1);
    }
}

fn run() -> Result<(), String> {
    let args: Vec<String> = std::env::args().skip(1).collect();
    match args.split_first() {
        None => run_for_the_app(),
        #[cfg(unix)]
        Some((command, rest)) if command == "serve" => {
            commitarium_host::service::serve(commitarium_host::service::data_dir(rest)?)
        }
        #[cfg(unix)]
        Some((command, rest)) if command == "status" => {
            let dir = commitarium_host::service::data_dir(rest)?;
            println!("{}", commitarium_host::service::status(&dir)?);
            Ok(())
        }
        Some((command, _)) => Err(format!("unknown command: {command}")),
    }
}

fn run_for_the_app() -> Result<(), String> {
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
    let shared = Arc::new(Shared::new(launch.data_dir.clone(), Bus::default()));
    shared.begin_background_work();
    jobs::watch(shared.clone());
    let access = Access::new(launch, port);
    let serving = shared.clone();
    thread::spawn(move || server::serve(http, access, serving));

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
    shared.stop_previews_and_logins();
    Ok(())
}
