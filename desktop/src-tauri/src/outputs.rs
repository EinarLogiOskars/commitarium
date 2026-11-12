//! What Mayka's helpers make (HELPERS_PLAN.md): saving a file where the user
//! chooses, opening one the app cannot show in the user's own app for it,
//! and exporting it as a PDF through the system's print dialog.
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
use tauri_plugin_opener::OpenerExt;

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
            match window.print() {
                Ok(()) => close_after_printing(window),
                Err(error) => eprintln!("[outputs] could not open the print dialog: {error}"),
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

/// How often a print window looks whether its dialog is still open.
#[cfg(target_os = "macos")]
const PRINT_POLL: std::time::Duration = std::time::Duration::from_millis(400);

/// Closes a print window once its print dialog is gone. On macOS the dialog
/// is a sheet that `print()` starts and returns from at once, with no word
/// of when it ends (wry passes no delegate), so the window looks for the
/// sheet: once it was there and has been gone a moment (Save as PDF swaps
/// one sheet for another), printing is done or cancelled and the window
/// closes. A dialog that never shows leaves the window to the user, who
/// closes it as any other (⌘W). Elsewhere the window stays for the user to
/// close: WebView2's print dialog says nothing either.
#[cfg(target_os = "macos")]
fn close_after_printing(window: tauri::WebviewWindow) {
    use objc2_app_kit::NSWindow;
    /// Polls without a sheet before closing, after one was seen.
    const GONE_POLLS: u32 = 4;
    /// Polls to wait for the sheet to show at all.
    const SHOW_POLLS: u32 = 50;
    std::thread::spawn(move || {
        let mut seen = false;
        let mut quiet = 0;
        loop {
            std::thread::sleep(PRINT_POLL);
            let (sender, receiver) = std::sync::mpsc::channel();
            let asked = window.with_webview(move |platform| {
                // SAFETY: wry hands its NSWindow on the main thread, where
                // this closure runs.
                let ns_window = unsafe { &*(platform.ns_window() as *const NSWindow) };
                let _ = sender.send(ns_window.attachedSheet().is_some());
            });
            // The user closed the window, or the app is quitting.
            let Ok(sheet) = asked.map_err(|_| ()).and_then(|()| {
                receiver
                    .recv_timeout(std::time::Duration::from_secs(5))
                    .map_err(|_| ())
            }) else {
                return;
            };
            if sheet {
                seen = true;
                quiet = 0;
                continue;
            }
            quiet += 1;
            if (seen && quiet >= GONE_POLLS) || (!seen && quiet >= SHOW_POLLS) {
                break;
            }
        }
        if seen {
            let _ = window.close();
        }
    });
}

#[cfg(not(target_os = "macos"))]
fn close_after_printing(_window: tauri::WebviewWindow) {}

/// Kinds a helper's file may be opened as in the user's own app: documents,
/// sheets, slides, archives and media, never anything that runs (a script,
/// an app, an installer, or an Office file with macros).
const OPENABLE: &[&str] = &[
    "docx", "doc", "pptx", "ppt", "xlsx", "xls", "odt", "odp", "ods", "rtf", "pages", "numbers",
    "key", "epub", "pdf", "csv", "tsv", "txt", "md", "png", "jpg", "jpeg", "gif", "webp", "svg",
    "ico", "zip", "mp3", "wav", "mp4", "mov",
];

/// A file's name as written to the cache: its last part, without
/// separators, control characters or a leading dot.
fn cache_name(name: &str) -> String {
    let base = name.rsplit(['/', '\\']).next().unwrap_or_default();
    let cleaned: String = base
        .chars()
        .map(|c| if c.is_control() || c == ':' { '-' } else { c })
        .collect();
    let cleaned = cleaned.trim().trim_start_matches('.').trim();
    if cleaned.is_empty() {
        "output".into()
    } else {
        cleaned.chars().take(120).collect()
    }
}

fn openable(name: &str) -> bool {
    name.rsplit_once('.')
        .map(|(_, extension)| OPENABLE.contains(&extension.to_ascii_lowercase().as_str()))
        .unwrap_or(false)
}

/// Opens a file the app cannot show in the user's default app for it: the
/// contents come as the raw body and the name in `x-name`, as for Save, and
/// are written to the app's cache under the file's name first.
#[tauri::command]
pub async fn open_output(app: AppHandle, request: tauri::ipc::Request<'_>) -> Result<(), String> {
    let tauri::ipc::InvokeBody::Raw(contents) = request.body() else {
        return Err("the file's contents are missing".into());
    };
    let name = cache_name(
        request
            .headers()
            .get("x-name")
            .and_then(|value| value.to_str().ok())
            .unwrap_or_default(),
    );
    if !openable(&name) {
        return Err(format!(
            "Commitarium does not open {name} itself. Save it instead."
        ));
    }
    let directory = app
        .path()
        .app_cache_dir()
        .map_err(|e| e.to_string())?
        .join("outputs");
    std::fs::create_dir_all(&directory).map_err(|e| format!("could not open {name}: {e}"))?;
    let path = directory.join(&name);
    std::fs::write(&path, contents).map_err(|e| format!("could not open {name}: {e}"))?;
    app.opener()
        .open_path(path.to_string_lossy(), None::<&str>)
        .map_err(|e| format!("could not open {name}: {e}"))
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
    use super::{cache_name, openable, PrintPages};

    #[test]
    fn opened_files_keep_a_plain_name_and_only_document_kinds() {
        assert_eq!(cache_name("../../etc/plan.docx"), "plan.docx");
        assert_eq!(cache_name("a\\b\\.hidden.xlsx"), "hidden.xlsx");
        assert_eq!(cache_name("deck:\u{7}.pptx"), "deck--.pptx");
        assert_eq!(cache_name(".."), "output");
        assert!(openable("Plan.DOCX") && openable("data.zip"));
        assert!(!openable("run.command") && !openable("book.xlsm") && !openable("noext"));
    }

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
