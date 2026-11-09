//! What Mayka's helpers make (HELPERS_PLAN.md): saving a file where the user
//! chooses, and exporting it as a PDF through the system's print dialog.
//!
//! A helper's HTML is not Commitarium's: it never loads in the main window.
//! Export renders the page in a window of its own, served from a custom
//! scheme with scripts turned off, and that window is granted no commands
//! (no capability names it), so nothing in the page can reach Commitarium.

use std::collections::VecDeque;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Mutex;

use tauri::http::{header, Response, StatusCode};
use tauri::webview::PageLoadEvent;
use tauri::{AppHandle, Manager, Url, WebviewUrl, WebviewWindowBuilder};
use tauri_plugin_dialog::DialogExt;

/// The scheme print windows load their page from.
pub(crate) const PRINT_SCHEME: &str = "commitarium-print";

/// How many pages to print are kept; older ones are dropped.
const KEPT_PAGES: usize = 8;

/// The pages print windows show, by number.
#[derive(Default)]
pub(crate) struct PrintPages {
    next: AtomicU64,
    pages: Mutex<VecDeque<(u64, String)>>,
}

impl PrintPages {
    fn add(&self, html: String) -> u64 {
        let number = self.next.fetch_add(1, Ordering::Relaxed) + 1;
        let mut pages = self.pages.lock().unwrap_or_else(|e| e.into_inner());
        pages.push_back((number, html));
        while pages.len() > KEPT_PAGES {
            pages.pop_front();
        }
        number
    }

    fn get(&self, number: u64) -> Option<String> {
        let pages = self.pages.lock().unwrap_or_else(|e| e.into_inner());
        pages
            .iter()
            .find(|(n, _)| *n == number)
            .map(|(_, html)| html.clone())
    }
}

/// Serves a print window its page, with scripts and outside requests turned
/// off; images and fonts must be inline.
pub(crate) fn serve_print_page(app: &AppHandle, path: &str) -> Response<Vec<u8>> {
    let page = path
        .trim_start_matches('/')
        .parse::<u64>()
        .ok()
        .and_then(|number| app.state::<PrintPages>().get(number));
    match page {
        Some(html) => Response::builder()
            .status(StatusCode::OK)
            .header(header::CONTENT_TYPE, "text/html; charset=utf-8")
            .header(
                header::CONTENT_SECURITY_POLICY,
                "default-src 'none'; img-src data: blob:; style-src 'unsafe-inline'; font-src data:",
            )
            .body(html.into_bytes())
            .expect("a valid response"),
        None => Response::builder()
            .status(StatusCode::NOT_FOUND)
            .body(Vec::new())
            .expect("a valid response"),
    }
}

fn print_url(number: u64) -> Result<Url, String> {
    // Windows' WebView2 reaches custom schemes as http://<scheme>.localhost.
    let url = if cfg!(windows) {
        format!("http://{PRINT_SCHEME}.localhost/{number}")
    } else {
        format!("{PRINT_SCHEME}://localhost/{number}")
    };
    Url::parse(&url).map_err(|e| e.to_string())
}

/// Opens a page in a window of its own and the print dialog on it, where
/// the user saves it as a PDF. Async: creating a window on the main thread
/// deadlocks on Windows (apps.rs).
#[tauri::command]
pub async fn print_output(app: AppHandle, title: String, html: String) -> Result<(), String> {
    let number = app.state::<PrintPages>().add(html);
    let title = title.trim();
    let title = if title.is_empty() { "Export" } else { title };
    WebviewWindowBuilder::new(
        &app,
        format!("print-{number}"),
        WebviewUrl::CustomProtocol(print_url(number)?),
    )
    .title(title)
    .inner_size(860.0, 1000.0)
    .on_page_load(|window, payload| {
        if payload.event() == PageLoadEvent::Finished {
            if let Err(error) = window.print() {
                eprintln!("[outputs] could not open the print dialog: {error}");
            }
        }
    })
    .on_navigation(|url| {
        url.scheme() == PRINT_SCHEME || url.host_str() == Some("commitarium-print.localhost")
    })
    .on_new_window(|_, _| tauri::webview::NewWindowResponse::Deny)
    .build()
    .map(|_| ())
    .map_err(|e| e.to_string())
}

/// Saves a file where the user chooses, under its name. The contents come as
/// the request's raw body and the name in the `x-name` header, so a large
/// file is not sent as JSON. Answers whether it was saved (false when the
/// user cancelled).
#[tauri::command]
pub async fn save_output(app: AppHandle, request: tauri::ipc::Request<'_>) -> Result<bool, String> {
    let tauri::ipc::InvokeBody::Raw(contents) = request.body() else {
        return Err("the file's contents are missing".into());
    };
    let name = request
        .headers()
        .get("x-name")
        .and_then(|value| value.to_str().ok())
        .map(|name| name.replace(['/', '\\'], "-"))
        .unwrap_or_else(|| "output".into());
    let Some(target) = app
        .dialog()
        .file()
        .set_file_name(&name)
        .blocking_save_file()
    else {
        return Ok(false);
    };
    let path = target.into_path().map_err(|e| e.to_string())?;
    std::fs::write(&path, contents)
        .map_err(|e| format!("could not save {}: {e}", path.display()))?;
    Ok(true)
}

#[cfg(test)]
mod tests {
    use super::PrintPages;

    #[test]
    fn print_pages_keep_only_the_latest() {
        let pages = PrintPages::default();
        let first = pages.add("<p>1</p>".into());
        for n in 0..super::KEPT_PAGES {
            pages.add(format!("<p>{n}</p>"));
        }
        assert!(pages.get(first).is_none());
        assert_eq!(
            pages.get(first + super::KEPT_PAGES as u64).as_deref(),
            Some("<p>7</p>")
        );
    }
}
