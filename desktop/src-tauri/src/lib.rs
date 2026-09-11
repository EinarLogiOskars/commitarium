mod bootstrap;
mod docker;
mod handoff;
mod import;
mod profiles;
mod store;

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let profiles = profiles::ProfileManager::new();
    let shutdown_profiles = profiles.clone();
    tauri::Builder::default()
        .manage(profiles)
        .plugin(tauri_plugin_opener::init())
        // HTTP client for the frontend to reach the local coordinator. The
        // capability scope (see capabilities/default.json) restricts it to the
        // coordinator's loopback origin — the network trust seam.
        .plugin(tauri_plugin_http::init())
        // Native folder picker for project import.
        .plugin(tauri_plugin_dialog::init())
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
            import::inspect_folder,
            import::import_project,
            import::get_project_source,
            handoff::synchronize_feature_locally,
            handoff::plain_folder::synchronize_feature_to_folder,
            profiles::list_profiles,
            profiles::begin_login,
            profiles::submit_login_code,
            profiles::submit_api_key,
            profiles::cancel_login,
            profiles::verify_profile,
            profiles::disconnect_profile,
        ])
        .build(tauri::generate_context!())
        .expect("error while building tauri application")
        .run(move |_, event| {
            if matches!(
                event,
                tauri::RunEvent::Exit | tauri::RunEvent::ExitRequested { .. }
            ) {
                shutdown_profiles.shutdown();
            }
        });
}
