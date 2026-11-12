//! Apps (APPS_PLAN.md): a project the user uses as an app runs in its own
//! window. The window loads Commitarium's own page, told which app it shows;
//! that page frames the running app, so the app's code never reaches
//! Commitarium. The page is served from 127.0.0.1, same-site with the app,
//! so the app's sign-in works (app_pages.rs, ADR-029).
//!
//! A window shows either the real app, the one the user uses, or a preview:
//! a test copy with test data, which the page marks with a bar the app
//! cannot hide, and two ways to the real app (HELPERS_PLAN.md H5). On this
//! computer the real app opens in a window or the browser; on a server it is
//! the deployment, opened in a browser at its address (ADR-040), so every
//! window there shows the preview.

use std::collections::BTreeMap;
use std::fs;
use std::sync::Mutex;

use serde::Serialize;
use serde_json::{json, Value};
use tauri::webview::NewWindowResponse;
use tauri::{AppHandle, Manager, Url, WebviewUrl, WebviewWindowBuilder};
use tauri_plugin_opener::OpenerExt;

use crate::app_pages::AppPages;
use crate::host;

/// Checks a project ID, which is limited to letters, digits, and
/// underscores, so it is safe in a window label and a script.
fn valid_project_id(project_id: &str) -> Result<(), String> {
    let valid = project_id.starts_with("prj_")
        && project_id.len() <= 68
        && project_id
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '_');
    if valid {
        Ok(())
    } else {
        Err("not a project ID".into())
    }
}

/// A window label for an app: `app-` and the project ID, or `app-preview-`
/// for its preview, so both can be open at once. Both match the app page's
/// capability (`app-*`).
fn app_window_label(project_id: &str, preview: bool) -> Result<String, String> {
    valid_project_id(project_id)?;
    Ok(if preview {
        format!("app-preview-{project_id}")
    } else {
        format!("app-{project_id}")
    })
}

/// Runs in the app's frame: a click on a link to another site asks for a new
/// window instead, which open_link sends to the user's browser. Followed in
/// the frame, the site would mostly refuse to be framed (GitHub does) and
/// the click would seem to do nothing. A click the app handles itself is
/// left to it.
const LINKS_OUT: &str = r#"
if (window.top !== window) {
  document.addEventListener("click", (event) => {
    if (event.defaultPrevented || event.button !== 0) return;
    const link = event.target instanceof Element ? event.target.closest("a[href]") : null;
    if (!link || link.hasAttribute("download")) return;
    const url = new URL(link.href, location.href);
    const local = ["127.0.0.1", "localhost", "[::1]"].includes(url.hostname);
    if (!/^https?:$/.test(url.protocol) || local) return;
    event.preventDefault();
    window.open(url.href, "_blank", "noopener");
  });
}
"#;

/// A new window the app asks for opens in the user's browser: an app window
/// shows only its app.
fn open_link(app: &AppHandle, url: &Url) {
    if !matches!(url.scheme(), "http" | "https") {
        return;
    }
    if let Err(error) = app.opener().open_url(url.as_str(), None::<&str>) {
        eprintln!("[apps] could not open {url} in the browser: {error}");
    }
}

/// Opens an app's window, or brings it to the front when it is open: the
/// real app's, or with `preview` its preview's. On a server it is always the
/// preview, since the real app there is the deployment.
///
/// It must be async: a synchronous command runs on the main thread, and on
/// Windows creating a window there deadlocks the event loop (Tauri's
/// documented WebView2 limitation), leaving a blank window that cannot be
/// closed and a main window that stops answering.
#[tauri::command]
pub async fn open_app_window(
    app: AppHandle,
    project_id: String,
    title: String,
    preview: Option<bool>,
) -> Result<(), String> {
    let preview = preview.unwrap_or(false) || host::on_server(&app);
    let label = app_window_label(&project_id, preview)?;
    if !preview {
        remember_opened(&app, &project_id, Opened::Window);
    }
    if let Some(window) = app.get_webview_window(&label) {
        let _ = window.unminimize();
        return window.set_focus().map_err(|e| e.to_string());
    }
    let title = title.trim();
    let title = if title.is_empty() { "App" } else { title };
    let title = if preview {
        format!("{title} · Preview")
    } else {
        title.to_string()
    };
    // The ID is validated above, so it is safe inside the script. The page
    // reads which window it is from here, set before any of its code runs;
    // the framed app is another origin and cannot change it.
    let script = format!(
        "window.__COMMITARIUM_APP__ = \"{project_id}\"; window.__COMMITARIUM_PREVIEW__ = {preview};"
    );
    let page = app
        .try_state::<AppPages>()
        .ok_or("app windows are unavailable; see the desktop's log")?
        .page();
    WebviewWindowBuilder::new(&app, &label, WebviewUrl::External(page))
        .title(title)
        .inner_size(1100.0, 780.0)
        .min_inner_size(480.0, 360.0)
        .initialization_script(&script)
        .initialization_script_for_all_frames(LINKS_OUT)
        .on_new_window({
            let app = app.clone();
            move |url, _| {
                open_link(&app, &url);
                NewWindowResponse::Deny
            }
        })
        // Files dropped on a composer reach the page, to attach them.
        .disable_drag_drop_handler()
        .build()
        .map(|_| ())
        .map_err(|e| e.to_string())
}

/// How the user last opened an app's real app: the preview bar and the Apps
/// page put that button first.
#[derive(Clone, Copy, Debug, PartialEq, Serialize)]
#[serde(rename_all = "snake_case")]
enum Opened {
    Window,
    Browser,
}

impl Opened {
    fn name(self) -> &'static str {
        match self {
            Opened::Window => "window",
            Opened::Browser => "browser",
        }
    }

    fn read(name: &str) -> Option<Self> {
        match name {
            "window" => Some(Opened::Window),
            "browser" => Some(Opened::Browser),
            _ => None,
        }
    }
}

/// Where the choices are kept: a file of their own, since the main window
/// and app windows are different origins and share no browser storage.
const OPENED_FILE: &str = "real-app-opened.json";
/// Orders changes to the file.
static OPENED: Mutex<()> = Mutex::new(());

fn read_opened(app: &AppHandle) -> BTreeMap<String, String> {
    crate::desktop_services::app_data_dir(app)
        .ok()
        .and_then(|dir| fs::read(dir.join(OPENED_FILE)).ok())
        .and_then(|body| serde_json::from_slice(&body).ok())
        .unwrap_or_default()
}

fn last_opened(app: &AppHandle, project_id: &str) -> Option<Opened> {
    Opened::read(read_opened(app).get(project_id)?)
}

/// Remembers the choice. A choice that cannot be saved is only a lost
/// ordering, so it is logged, never in the way of opening.
fn remember_opened(app: &AppHandle, project_id: &str, how: Opened) {
    let Ok(_held) = OPENED.lock() else {
        return;
    };
    let mut choices = read_opened(app);
    if choices.get(project_id).map(String::as_str) == Some(how.name()) {
        return;
    }
    choices.insert(project_id.to_string(), how.name().to_string());
    let saved = crate::desktop_services::app_data_dir(app).and_then(|dir| {
        let body = serde_json::to_vec(&choices).map_err(|e| e.to_string())?;
        fs::write(dir.join(OPENED_FILE), body).map_err(|e| e.to_string())
    });
    if let Err(error) = saved {
        eprintln!("[apps] could not remember how an app was opened: {error}");
    }
}

/// What the preview bar and the Apps page need to offer the real app.
#[derive(Debug, PartialEq, Serialize)]
pub struct RealApp {
    /// Whether the window shows a server, where the real app is the
    /// deployment, opened in a browser.
    on_server: bool,
    /// On a server: whether the app is deployed.
    deployed: bool,
    /// Its address for the browser, when known: on this computer while it
    /// runs, on a server once it has an address.
    address: Option<String>,
    /// The button the user used last for it.
    last: Option<Opened>,
}

/// The address of a running preview's open port, as the windows pick it.
fn primary_url(status: &Value) -> Option<&str> {
    if status["state"] != "running" {
        return None;
    }
    let urls = status["urls"].as_array()?;
    urls.iter()
        .find(|u| u["open"] == true)
        .or_else(|| urls.first())
        .and_then(|u| u["url"].as_str())
}

/// The real app's address on this computer: its running copy, on loopback
/// over http only.
fn local_address(status: &Value) -> Option<String> {
    let url = Url::parse(primary_url(status)?).ok()?;
    (url.scheme() == "http" && url.host_str() == Some("127.0.0.1")).then(|| url.to_string())
}

/// Whether a server's list of deployments has the project deployed.
fn is_deployed(deployments: &Value, project_id: &str) -> bool {
    deployments.as_array().is_some_and(|all| {
        all.iter()
            .any(|d| d["project_id"] == project_id && !d["current"].is_null())
    })
}

/// The project's main address among a server's published apps, http(s) only.
fn published_address(publishing: &Value, project_id: &str) -> Option<String> {
    let found = publishing["apps"]
        .as_array()?
        .iter()
        .find(|a| a["project_id"] == project_id)?;
    let url = Url::parse(found["url"].as_str()?).ok()?;
    matches!(url.scheme(), "http" | "https").then(|| url.to_string())
}

/// Finds the real app's address from the host, never from a page.
async fn find_real_app(app: &AppHandle, project_id: &str) -> Result<RealApp, String> {
    valid_project_id(project_id)?;
    let on_server = host::on_server(app);
    let (deployed, address) = if on_server {
        let deployments = host::get(app, "/v1/deployments").await?;
        let deployed = is_deployed(&deployments, project_id);
        let address = if deployed {
            published_address(&host::get(app, "/v1/publishing").await?, project_id)
        } else {
            None
        };
        (deployed, address)
    } else {
        let body = json!({ "project_id": project_id, "feature_id": null });
        let status = host::post(app, "/v1/previews/status", body).await?;
        (false, local_address(&status))
    };
    Ok(RealApp {
        on_server,
        deployed,
        address,
        last: last_opened(app, project_id),
    })
}

/// The real app of a project, for the preview bar and the Apps page.
#[tauri::command]
pub async fn real_app(app: AppHandle, project_id: String) -> Result<RealApp, String> {
    find_real_app(&app, &project_id).await
}

/// Opens the real app in the user's browser. The address is looked up here,
/// so a page (an app window's is a remote origin) can only open an app's own
/// known address, never one it names.
#[tauri::command]
pub async fn open_real_app_in_browser(app: AppHandle, project_id: String) -> Result<(), String> {
    let real = find_real_app(&app, &project_id).await?;
    let Some(address) = real.address else {
        return Err(if real.on_server {
            "The app has no address on the server yet.".into()
        } else {
            "The app isn't running yet.".into()
        });
    };
    app.opener()
        .open_url(address.as_str(), None::<&str>)
        .map_err(|e| format!("could not open the browser: {e}"))?;
    remember_opened(&app, &project_id, Opened::Browser);
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn app_windows_are_named_by_a_valid_project_id_only() {
        assert_eq!(
            app_window_label("prj_abc123", false).unwrap(),
            "app-prj_abc123"
        );
        assert_eq!(
            app_window_label("prj_abc123", true).unwrap(),
            "app-preview-prj_abc123"
        );
        assert!(app_window_label("prj_x\";alert(1)", false).is_err());
        assert!(app_window_label("other", true).is_err());
    }

    #[test]
    fn the_local_address_is_the_running_open_port_on_loopback() {
        let status = json!({
            "state": "running",
            "urls": [
                { "url": "http://127.0.0.1:5000", "open": false },
                { "url": "http://127.0.0.1:5001", "open": true },
            ],
        });
        assert_eq!(
            local_address(&status).as_deref(),
            Some("http://127.0.0.1:5001/")
        );
        let first = json!({ "state": "running", "urls": [{ "url": "http://127.0.0.1:5000", "open": false }] });
        assert_eq!(
            local_address(&first).as_deref(),
            Some("http://127.0.0.1:5000/")
        );
        let starting = json!({ "state": "starting", "urls": [{ "url": "http://127.0.0.1:5000", "open": true }] });
        assert_eq!(local_address(&starting), None);
        assert_eq!(local_address(&Value::Null), None);
        let elsewhere = json!({ "state": "running", "urls": [{ "url": "http://evil.example:80", "open": true }] });
        assert_eq!(local_address(&elsewhere), None);
        let other_scheme =
            json!({ "state": "running", "urls": [{ "url": "file:///etc/passwd", "open": true }] });
        assert_eq!(local_address(&other_scheme), None);
    }

    #[test]
    fn a_server_app_is_deployed_while_it_has_a_current_version() {
        let deployments = json!([
            { "project_id": "prj_a", "current": { "commit_id": "c1", "deployed_at": 1 } },
            { "project_id": "prj_b", "current": null },
        ]);
        assert!(is_deployed(&deployments, "prj_a"));
        assert!(!is_deployed(&deployments, "prj_b"));
        assert!(!is_deployed(&deployments, "prj_c"));
    }

    #[test]
    fn a_server_app_opens_at_its_main_published_address_only() {
        let publishing = json!({ "apps": [
            { "project_id": "prj_a", "url": "https://todo.example.com" },
            { "project_id": "prj_b", "url": "" },
            { "project_id": "prj_c", "url": "javascript:alert(1)" },
        ]});
        assert_eq!(
            published_address(&publishing, "prj_a").as_deref(),
            Some("https://todo.example.com/")
        );
        assert_eq!(published_address(&publishing, "prj_b"), None);
        assert_eq!(published_address(&publishing, "prj_c"), None);
        assert_eq!(published_address(&publishing, "prj_d"), None);
    }

    #[test]
    fn choices_are_read_back_by_name() {
        for how in [Opened::Window, Opened::Browser] {
            assert_eq!(Opened::read(how.name()), Some(how));
        }
        assert_eq!(Opened::read("other"), None);
    }
}
