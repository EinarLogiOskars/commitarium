//! The apps page (APPS_PAGE_PLAN.md, A1): one address, `apps.<domain>`, that
//! lists a server's apps and opens them in a frame. The host serves it on a
//! loopback port of its own, apart from its API, and Caddy puts it behind the
//! password or company sign-in the owner chose. It only reads: the listed
//! apps for whoever is asking, and the page that shows them.

use crate::deployments::Deployments;
use crate::publishing::Publishing;
use std::thread;
use tiny_http::{Header, Request, Response, Server};

/// Loopback only; Caddy is the way in.
pub const PORT: u16 = 7444;

const PAGE: &str = include_str!("apps_page.html");

/// Serves the apps page for as long as the host runs.
pub fn serve(publishing: Publishing, deployments: Deployments) {
    thread::spawn(move || {
        let server = match Server::http(("127.0.0.1", PORT)) {
            Ok(server) => server,
            Err(error) => {
                eprintln!("[apps page] could not listen on port {PORT}: {error}");
                return;
            }
        };
        for request in server.incoming_requests() {
            answer(request, &publishing, &deployments);
        }
    });
}

fn answer(request: Request, publishing: &Publishing, deployments: &Deployments) {
    if request.method() != &tiny_http::Method::Get {
        let _ = request.respond(Response::empty(405));
        return;
    }
    // Caddy removes this from every request, then sets it only from the
    // gate's answer: it cannot be forged.
    let groups = request
        .headers()
        .iter()
        .find(|header| header.field.equiv("X-Commitarium-Groups"))
        .map(|header| header.value.as_str().to_string());
    let path = request.url().split('?').next().unwrap_or("/").to_string();
    let response = if path == "/api/session" {
        // Reaching here means Caddy let the request through: behind company
        // sign-in, the session is still good. An expired one never gets
        // here; the page sees the gate's redirect instead (A2).
        json(200, r#"{"signed_in":true}"#)
    } else if path == "/api/apps" {
        match publishing.catalog(deployments, groups.as_deref()) {
            Ok(Some(apps)) => json(200, &serde_json::json!({ "apps": apps }).to_string()),
            Ok(None) => json(404, r#"{"error":"the apps page is off"}"#),
            Err(error) => {
                eprintln!("[apps page] {error}");
                json(500, r#"{"error":"the apps could not be read"}"#)
            }
        }
    } else {
        // Every other path is the page: `/<name>` opens that app in it.
        Response::from_string(PAGE)
            .with_header(header("Content-Type", "text/html; charset=utf-8"))
            .with_header(header("Content-Security-Policy", "frame-ancestors 'none'"))
            .with_header(header("Cache-Control", "no-store"))
    };
    let _ = request.respond(response);
}

fn json(status: u16, body: &str) -> Response<std::io::Cursor<Vec<u8>>> {
    Response::from_string(body)
        .with_status_code(status)
        .with_header(header("Content-Type", "application/json"))
        .with_header(header("Cache-Control", "no-store"))
}

fn header(name: &str, value: &str) -> Header {
    Header::from_bytes(name.as_bytes(), value.as_bytes()).expect("a valid header")
}
