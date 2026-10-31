//! The host's HTTP API, on loopback. A port here is open to every local
//! process and any page that can reach it, so every route sits behind one
//! check (`Access::allows`): the exact Host, a known Origin, and the launch's
//! token. No route answers without it, and no CORS header is ever sent.

use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};
use std::sync::Arc;
use std::thread;
use tiny_http::{Header, Request, Response, Server};

use crate::{disk, docker};

/// What the host is told at launch, over stdin, so it never shows in a
/// process listing.
#[derive(Deserialize, Serialize)]
pub struct Launch {
    pub token: String,
    pub origins: Vec<String>,
    pub data_dir: PathBuf,
}

/// The host's first and only line on stdout: where it listens and what it is.
#[derive(Debug, Deserialize, PartialEq, Serialize)]
pub struct Ready {
    pub port: u16,
    pub version: String,
}

pub struct Access {
    token: String,
    origins: Vec<String>,
    host: String,
}

impl Access {
    pub fn new(launch: Launch, port: u16) -> Self {
        Self {
            token: launch.token,
            origins: launch.origins,
            host: format!("127.0.0.1:{port}"),
        }
    }

    /// `host`, `origin`, and `authorization` are the request's headers. A
    /// program sends no Origin; a page cannot leave it out, so one from
    /// elsewhere is refused even with the right token.
    pub fn allows(
        &self,
        host: Option<&str>,
        origin: Option<&str>,
        authorization: Option<&str>,
    ) -> bool {
        let known_host = host == Some(self.host.as_str());
        let known_origin = origin.is_none_or(|origin| self.origins.iter().any(|o| o == origin));
        let token = authorization
            .and_then(|value| value.strip_prefix("Bearer "))
            .unwrap_or("");
        !self.token.is_empty()
            && known_host
            && known_origin
            && same(token.as_bytes(), self.token.as_bytes())
    }
}

/// Compares without stopping at the first difference.
fn same(a: &[u8], b: &[u8]) -> bool {
    a.len() == b.len() && a.iter().zip(b).fold(0, |diff, (x, y)| diff | (x ^ y)) == 0
}

fn header<'a>(request: &'a Request, name: &'static str) -> Option<&'a str> {
    request
        .headers()
        .iter()
        .find(|header| header.field.equiv(name))
        .map(|header| header.value.as_str())
}

fn json(status: u16, body: String) -> Response<std::io::Cursor<Vec<u8>>> {
    let content_type = Header::from_bytes("Content-Type", "application/json")
        .expect("the content type is a valid header");
    Response::from_string(body)
        .with_status_code(status)
        .with_header(content_type)
}

fn failure(status: u16, message: &str) -> Response<std::io::Cursor<Vec<u8>>> {
    json(status, serde_json::json!({ "error": message }).to_string())
}

fn respond_with<T: Serialize>(result: Result<T, String>) -> Response<std::io::Cursor<Vec<u8>>> {
    match result.and_then(|value| serde_json::to_string(&value).map_err(|e| e.to_string())) {
        Ok(body) => json(200, body),
        Err(error) => failure(500, &error),
    }
}

fn respond(path: &str, data_dir: &Path) -> Response<std::io::Cursor<Vec<u8>>> {
    match path {
        "/v1/version" => json(
            200,
            serde_json::json!({ "version": env!("CARGO_PKG_VERSION") }).to_string(),
        ),
        "/v1/stack/status" => respond_with(docker::stack_status()),
        "/v1/docker/probe" => respond_with(Ok(docker::docker_probe())),
        "/v1/disk/space" => respond_with(disk::disk_space_probe(data_dir)),
        _ => failure(404, "not found"),
    }
}

/// Serves requests, each on its own thread, because Docker can be slow.
pub fn serve(server: Server, access: Access, data_dir: PathBuf) {
    let access = Arc::new(access);
    let data_dir = Arc::new(data_dir);
    for request in server.incoming_requests() {
        let access = access.clone();
        let data_dir = data_dir.clone();
        thread::spawn(move || {
            let allowed = access.allows(
                header(&request, "Host"),
                header(&request, "Origin"),
                header(&request, "Authorization"),
            );
            let response = if !allowed {
                failure(401, "unauthorized")
            } else if *request.method() != tiny_http::Method::Get {
                failure(405, "method not allowed")
            } else {
                respond(request.url(), &data_dir)
            };
            let _ = request.respond(response);
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn access() -> Access {
        Access::new(
            Launch {
                token: "secret".into(),
                origins: vec!["tauri://localhost".into()],
                data_dir: PathBuf::new(),
            },
            4000,
        )
    }

    const HOST: Option<&str> = Some("127.0.0.1:4000");
    const BEARER: Option<&str> = Some("Bearer secret");

    #[test]
    fn the_app_with_its_token_is_let_in() {
        assert!(access().allows(HOST, Some("tauri://localhost"), BEARER));
        assert!(access().allows(HOST, None, BEARER));
    }

    #[test]
    fn a_missing_or_wrong_token_is_refused() {
        assert!(!access().allows(HOST, None, None));
        assert!(!access().allows(HOST, None, Some("Bearer wrong!")));
        assert!(!access().allows(HOST, None, Some("secret")));
    }

    #[test]
    fn another_origin_is_refused_even_with_the_token() {
        assert!(!access().allows(HOST, Some("http://127.0.0.1:5000"), BEARER));
        assert!(!access().allows(HOST, Some("null"), BEARER));
    }

    #[test]
    fn a_name_rebound_to_loopback_is_refused() {
        assert!(!access().allows(Some("evil.example:4000"), None, BEARER));
        assert!(!access().allows(None, None, BEARER));
    }

    #[test]
    fn an_unconfigured_token_lets_nobody_in() {
        let access = Access::new(
            Launch {
                token: String::new(),
                origins: vec![],
                data_dir: PathBuf::new(),
            },
            4000,
        );
        assert!(!access.allows(HOST, None, Some("Bearer ")));
    }
}
