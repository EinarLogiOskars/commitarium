//! The pages of app windows, served from 127.0.0.1 (ADR-029). An app's
//! preview runs on 127.0.0.1 too, so the app its window frames is same-site
//! with the page around it: WebKit then keeps the app's cookies and storage,
//! and its sign-in works. Same-site is not same-origin: the port differs, so
//! the app still cannot read the page or call Commitarium's commands.
//!
//! The page is Commitarium's own frontend. In a release a small server on a
//! random 127.0.0.1 port serves the bundled files, and only those; in
//! development Vite serves them on 127.0.0.1. Either way the page is a
//! remote origin to Tauri, so it reaches only the commands its capability
//! grants (`app-page` in permissions/windows.toml), never Docker control,
//! backups, imports, or publishing.

use std::thread;

use tauri::ipc::CapabilityBuilder;
use tauri::{AppHandle, Manager, Url};

/// The coordinator, which the page calls through the HTTP plugin.
const COORDINATOR: &str = "http://127.0.0.1:8080/*";

/// Where the app windows' pages are served from.
pub(crate) struct AppPages {
    base: Url,
}

impl AppPages {
    /// The page an app window loads.
    pub(crate) fn page(&self) -> Url {
        self.base.join("index.html").expect("a valid base URL")
    }
}

/// Serves the pages (or finds Vite's) and grants their origin its commands.
/// App windows cannot open without it, so a failure is logged and the rest
/// of Commitarium runs on.
pub(crate) fn start(app: &AppHandle) {
    let base = if tauri::is_dev() {
        dev_base(app)
    } else {
        serve(app.clone())
    };
    let base = match base {
        Ok(base) => base,
        Err(error) => {
            eprintln!("[app-pages] app windows are unavailable: {error}");
            return;
        }
    };
    if let Err(error) = app.add_capability(capability(&base)) {
        eprintln!("[app-pages] could not grant the app windows' page its commands: {error}");
        return;
    }
    app.manage(AppPages { base });
}

/// The capability of an app window's page: its exact origin, port included,
/// so the app on another port matches none of it.
fn capability(base: &Url) -> CapabilityBuilder {
    CapabilityBuilder::new("app-page")
        .remote(origin_pattern(base))
        .local(false)
        .window("app-*")
        .permission("core:default")
        .permission("opener:default")
        .permission_scoped(
            "http:default",
            vec![serde_json::json!({ "url": COORDINATOR })],
            Vec::<serde_json::Value>::new(),
        )
        .permission("app-page")
}

/// The page's origin as a URL pattern: scheme, host, and port exactly.
fn origin_pattern(base: &Url) -> String {
    format!("{}*", base.as_str())
}

/// Vite's address, on 127.0.0.1 (vite.config.ts listens there).
fn dev_base(app: &AppHandle) -> Result<Url, String> {
    let mut url = app
        .config()
        .build
        .dev_url
        .clone()
        .ok_or("no devUrl configured")?;
    url.set_host(Some("127.0.0.1"))
        .map_err(|e| format!("devUrl: {e}"))?;
    url.set_path("/");
    Ok(url)
}

/// Serves the bundled frontend on a random 127.0.0.1 port.
fn serve(app: AppHandle) -> Result<Url, String> {
    let server = tiny_http::Server::http("127.0.0.1:0").map_err(|e| e.to_string())?;
    let port = server
        .server_addr()
        .to_ip()
        .ok_or("the server has no IP address")?
        .port();
    let host = format!("127.0.0.1:{port}");
    let base = Url::parse(&format!("http://{host}/")).map_err(|e| e.to_string())?;
    let resolver = app.asset_resolver();
    thread::Builder::new()
        .name("app-pages".into())
        .spawn(move || {
            for request in server.incoming_requests() {
                let host_header = request
                    .headers()
                    .iter()
                    .find(|h| h.field.equiv("Host"))
                    .map(|h| h.value.as_str().to_string());
                let reply = answer(
                    request.method(),
                    host_header.as_deref(),
                    request.url(),
                    &host,
                    |path| {
                        resolver
                            .get(path.to_string())
                            .map(|asset| (asset.bytes, asset.mime_type))
                    },
                );
                let _ = request.respond(reply.into_response());
            }
        })
        .map_err(|e| e.to_string())?;
    Ok(base)
}

/// What the server answers, kept apart from the socket to be tested.
#[derive(Debug)]
struct Reply {
    status: u16,
    content_type: String,
    body: Vec<u8>,
    head: bool,
}

impl Reply {
    fn error(status: u16) -> Self {
        Reply {
            status,
            content_type: "text/plain; charset=utf-8".into(),
            body: Vec::new(),
            head: false,
        }
    }

    fn into_response(self) -> tiny_http::Response<std::io::Cursor<Vec<u8>>> {
        let length = self.body.len();
        let body = if self.head { Vec::new() } else { self.body };
        let mut response = tiny_http::Response::from_data(body).with_status_code(self.status);
        for (field, value) in headers(&self.content_type) {
            if let Ok(header) = tiny_http::Header::from_bytes(field.as_bytes(), value.as_bytes()) {
                response.add_header(header);
            }
        }
        if self.head {
            if let Ok(header) =
                tiny_http::Header::from_bytes(&b"Content-Length"[..], length.to_string().as_bytes())
            {
                response.add_header(header);
            }
        }
        response
    }
}

/// Every response's headers. The page is never framed (an app must not
/// wrap it to borrow its commands), never sniffed, and never cached, so an
/// update's files replace the old ones.
fn headers(content_type: &str) -> Vec<(&'static str, String)> {
    vec![
        ("Content-Type", content_type.to_string()),
        ("Content-Security-Policy", "frame-ancestors 'none'".into()),
        ("X-Frame-Options", "DENY".into()),
        ("X-Content-Type-Options", "nosniff".into()),
        ("Cache-Control", "no-store".into()),
        ("Referrer-Policy", "no-referrer".into()),
    ]
}

/// Answers one request: only GET and HEAD, only for this server's own host
/// (no other name rebound to 127.0.0.1), and only the bundled files.
fn answer(
    method: &tiny_http::Method,
    host_header: Option<&str>,
    url: &str,
    host: &str,
    resolve: impl Fn(&str) -> Option<(Vec<u8>, String)>,
) -> Reply {
    let head = match method {
        tiny_http::Method::Get => false,
        tiny_http::Method::Head => true,
        _ => return Reply::error(405),
    };
    if host_header != Some(host) {
        return Reply::error(421);
    }
    let path = url.split(['?', '#']).next().unwrap_or("/");
    if !path.starts_with('/') || path.contains("..") || path.contains('\\') {
        return Reply::error(404);
    }
    let path = if path == "/" { "/index.html" } else { path };
    match resolve(path) {
        Some((body, content_type)) => Reply {
            status: 200,
            content_type,
            body,
            head,
        },
        None => Reply::error(404),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const HOST: &str = "127.0.0.1:4100";

    fn files(path: &str) -> Option<(Vec<u8>, String)> {
        (path == "/index.html" || path == "/assets/app.js")
            .then(|| (path.as_bytes().to_vec(), "text/html".to_string()))
    }

    fn get(host: Option<&str>, url: &str) -> Reply {
        answer(&tiny_http::Method::Get, host, url, HOST, files)
    }

    #[test]
    fn serves_the_bundled_files_and_the_page_at_the_root() {
        let page = get(Some(HOST), "/?app=1");
        assert_eq!(
            (page.status, page.body.as_slice()),
            (200, &b"/index.html"[..])
        );
        assert_eq!(get(Some(HOST), "/assets/app.js").status, 200);
        assert_eq!(get(Some(HOST), "/missing.js").status, 404);
    }

    #[test]
    fn answers_only_its_own_host() {
        assert_eq!(get(Some("localhost:4100"), "/").status, 421);
        assert_eq!(get(Some("evil.example:4100"), "/").status, 421);
        assert_eq!(get(None, "/").status, 421);
    }

    #[test]
    fn serves_nothing_outside_the_bundle_and_reads_only() {
        assert_eq!(get(Some(HOST), "/../secret").status, 404);
        assert_eq!(get(Some(HOST), "/assets/..%2f..\\x").status, 404);
        let post = answer(&tiny_http::Method::Post, Some(HOST), "/", HOST, files);
        assert_eq!(post.status, 405);
        let head = answer(&tiny_http::Method::Head, Some(HOST), "/", HOST, files);
        assert!(head.head && head.status == 200);
    }

    #[test]
    fn the_page_is_never_framed_sniffed_or_cached() {
        let headers = headers("text/html");
        for expected in [
            ("Content-Security-Policy", "frame-ancestors 'none'"),
            ("X-Frame-Options", "DENY"),
            ("X-Content-Type-Options", "nosniff"),
            ("Cache-Control", "no-store"),
        ] {
            assert!(
                headers.iter().any(|(f, v)| (*f, v.as_str()) == expected),
                "missing {expected:?}"
            );
        }
    }

    #[test]
    fn the_capability_is_bound_to_the_exact_origin() {
        let base = Url::parse("http://127.0.0.1:4100/").unwrap();
        assert_eq!(origin_pattern(&base), "http://127.0.0.1:4100/*");
    }

    /// The commands `lib.rs` registers, `build.rs` lists, and the main
    /// window's set grants stay the same; the app page's are among them.
    #[test]
    fn every_command_is_listed_and_granted_to_the_main_window() {
        let between = |text: &'static str, start: &str, end: &str| -> Vec<String> {
            let from = text.find(start).expect(start) + start.len();
            let to = from + text[from..].find(end).expect(end);
            text[from..to]
                .split(',')
                .map(|item| {
                    item.trim()
                        .trim_matches('"')
                        .rsplit("::")
                        .next()
                        .unwrap()
                        .trim()
                        .to_string()
                })
                .filter(|item| !item.is_empty())
                .collect()
        };
        let mut handled = between(include_str!("lib.rs"), "generate_handler![", "]");
        let mut listed = between(include_str!("../build.rs"), "COMMANDS: &[&str] = &[", "];");
        let windows = include_str!("../permissions/windows.toml");
        let set = |name: &str| -> Vec<String> {
            let start = windows
                .find(&format!("identifier = \"{name}\""))
                .expect(name);
            let rest = &windows[start..];
            let from = rest.find("permissions = [").unwrap() + "permissions = [".len();
            let to = from + rest[from..].find(']').unwrap();
            rest[from..to]
                .split(',')
                .map(|item| {
                    item.trim()
                        .trim_matches('"')
                        .trim_start_matches("allow-")
                        .replace('-', "_")
                })
                .filter(|item| !item.is_empty())
                .collect()
        };
        let mut main = set("main-window");
        handled.sort();
        listed.sort();
        main.sort();
        assert_eq!(handled, listed, "build.rs COMMANDS differs from lib.rs");
        assert_eq!(handled, main, "the main-window set differs from lib.rs");
        for command in set("app-page") {
            assert!(
                handled.contains(&command),
                "app-page grants unknown {command}"
            );
        }
    }
}
