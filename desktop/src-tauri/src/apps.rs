//! Apps (APPS_PLAN.md): a project the user uses as an app runs in its own
//! window. The window loads Commitarium's own page, told which app it shows;
//! that page frames the running app, so the app's code never reaches
//! Commitarium. The page is served from 127.0.0.1, same-site with the app,
//! so the app's sign-in works (app_pages.rs, ADR-029).

use tauri::webview::NewWindowResponse;
use tauri::{AppHandle, Manager, Url, WebviewUrl, WebviewWindowBuilder};
use tauri_plugin_opener::OpenerExt;

use crate::app_pages::AppPages;

/// A window label for an app: `app-` and the project ID, which is already
/// limited to letters, digits, and underscores.
fn app_window_label(project_id: &str) -> Result<String, String> {
    let valid = project_id.starts_with("prj_")
        && project_id.len() <= 68
        && project_id
            .chars()
            .all(|c| c.is_ascii_alphanumeric() || c == '_');
    if !valid {
        return Err("not a project ID".into());
    }
    Ok(format!("app-{project_id}"))
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

/// Opens an app's window, or brings it to the front when it is open.
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
) -> Result<(), String> {
    let label = app_window_label(&project_id)?;
    if let Some(window) = app.get_webview_window(&label) {
        let _ = window.unminimize();
        return window.set_focus().map_err(|e| e.to_string());
    }
    let title = title.trim();
    let title = if title.is_empty() { "App" } else { title };
    // The ID is validated above, so it is safe inside the script.
    let script = format!("window.__COMMITARIUM_APP__ = \"{project_id}\";");
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

#[cfg(test)]
mod tests {
    use super::app_window_label;

    #[test]
    fn app_windows_are_named_by_a_valid_project_id_only() {
        assert_eq!(app_window_label("prj_abc123").unwrap(), "app-prj_abc123");
        assert!(app_window_label("prj_x\";alert(1)").is_err());
        assert!(app_window_label("other").is_err());
    }
}
