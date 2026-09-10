mod docker;
mod store;

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        // HTTP client for the frontend to reach the local coordinator. The
        // capability scope (see capabilities/default.json) restricts it to the
        // coordinator's loopback origin — the network trust seam.
        .plugin(tauri_plugin_http::init())
        // The renderer can invoke ONLY the commands listed here. This explicit
        // set is the trust boundary: no arbitrary shell, Docker, or filesystem
        // access reaches the untrusted UI.
        .invoke_handler(tauri::generate_handler![
            docker::docker_probe,
            docker::stack_up,
            docker::stack_down,
            docker::stack_update,
            docker::stack_status,
            store::load_ui_state,
            store::save_ui_state,
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
