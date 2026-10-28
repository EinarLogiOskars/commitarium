//! A snapshot of what the user sees in a window: the page as the webview
//! draws it, framed apps included, as a PNG. It is the webview's own
//! snapshot, so it needs no screen-recording permission and saves no file.
//! Only macOS takes one for now; elsewhere the desktop hides the button.

use tauri::ipc::Response;

/// Whether this platform can take a window's snapshot.
#[tauri::command]
pub fn webview_snapshot_supported() -> bool {
    cfg!(target_os = "macos")
}

/// The calling window's page as a PNG.
#[tauri::command]
pub async fn webview_snapshot(webview: tauri::Webview) -> Result<Response, String> {
    let (sender, receiver) = tokio::sync::oneshot::channel::<Result<Vec<u8>, String>>();
    capture(&webview, sender)?;
    let png = receiver
        .await
        .map_err(|_| "the window closed before its snapshot was taken".to_string())??;
    Ok(Response::new(png))
}

#[cfg(target_os = "macos")]
fn capture(
    webview: &tauri::Webview,
    sender: tokio::sync::oneshot::Sender<Result<Vec<u8>, String>>,
) -> Result<(), String> {
    use block2::RcBlock;
    use objc2_app_kit::{NSBitmapImageFileType, NSBitmapImageRep, NSImage};
    use objc2_foundation::{NSDictionary, NSError};
    use objc2_web_kit::WKWebView;
    use std::sync::Mutex;

    // The block may in principle run more than once; only the first answer
    // counts.
    let sender = Mutex::new(Some(sender));
    webview
        .with_webview(move |platform| {
            // Runs on the main thread, where WebKit must be called.
            let view = unsafe { &*(platform.inner() as *const WKWebView) };
            let done = RcBlock::new(move |image: *mut NSImage, _error: *mut NSError| {
                let png = unsafe { image.as_ref() }
                    .and_then(|image| image.TIFFRepresentation())
                    .and_then(|tiff| NSBitmapImageRep::imageRepWithData(&tiff))
                    .and_then(|bitmap| unsafe {
                        bitmap.representationUsingType_properties(
                            NSBitmapImageFileType::PNG,
                            &NSDictionary::new(),
                        )
                    })
                    .map(|data| data.to_vec())
                    .ok_or_else(|| "the window could not be captured".to_string());
                if let Some(sender) = sender.lock().ok().and_then(|mut s| s.take()) {
                    let _ = sender.send(png);
                }
            });
            unsafe { view.takeSnapshotWithConfiguration_completionHandler(None, &done) };
        })
        .map_err(|e| e.to_string())
}

#[cfg(not(target_os = "macos"))]
fn capture(
    _webview: &tauri::Webview,
    _sender: tokio::sync::oneshot::Sender<Result<Vec<u8>, String>>,
) -> Result<(), String> {
    Err("taking a snapshot of the window is not available on this system yet".to_string())
}
