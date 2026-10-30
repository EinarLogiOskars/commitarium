mod agents;
mod app_pages;
mod apps;
mod bootstrap;
mod cleanup;
mod cli_updates;
mod coordinator_build;
mod desktop_services;
mod docker;
mod environment;
mod git_providers;
mod handoff;
mod host;
mod import;
mod preview;
mod preview_check;
mod preview_compose;
mod process;
mod profiles;
mod snapshot;
mod store;
mod updater;

use tauri::{Emitter, Manager};

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    let profiles = profiles::ProfileManager::new();
    let shutdown_profiles = profiles.clone();
    let previews = preview::PreviewManager::default();
    let shutdown_previews = previews.clone();
    tauri::Builder::default()
        .manage(profiles)
        .manage(previews)
        .manage(desktop_services::ExitCoordinator::default())
        .manage(desktop_services::NotificationCoordinator::default())
        .manage(desktop_services::BackupCoordinator::default())
        .manage(updater::UpdateCoordinator::default())
        .manage(cli_updates::CliUpdater::default())
        .manage(coordinator_build::CoordinatorBuild::default())
        .manage(host::Host::default())
        .plugin(tauri_plugin_opener::init())
        // HTTP client for the frontend to reach the local coordinator. The
        // capability scope (see capabilities/default.json) restricts it to the
        // coordinator's loopback origin — the network trust seam.
        .plugin(tauri_plugin_http::init())
        // Native folder picker for project import.
        .plugin(tauri_plugin_dialog::init())
        .plugin(tauri_plugin_notification::init())
        .plugin(tauri_plugin_updater::Builder::new().build())
        .setup(|app| {
            docker::prepare_runtime(app.handle()).map_err(std::io::Error::other)?;
            // After the runtime, because the host inherits its environment.
            host::launch(app.handle());
            // No preview runs yet in this process, so whatever is left is
            // from an earlier one.
            preview::sweep_leftovers();
            // The agents' CLIs stay current in the background, from launch on.
            cli_updates::spawn(app.handle().clone());
            // App windows' pages, served same-site with the apps (ADR-029).
            app_pages::start(app.handle());
            Ok(())
        })
        // The renderer can invoke ONLY the commands listed here. This explicit
        // set is the trust boundary: no arbitrary shell, Docker, or filesystem
        // access reaches the untrusted UI.
        .invoke_handler(tauri::generate_handler![
            apps::open_app_window,
            snapshot::webview_snapshot,
            snapshot::webview_snapshot_supported,
            cli_updates::agent_cli_status,
            coordinator_build::coordinator_freshness,
            coordinator_build::rebuild_coordinator,
            preview::prune_session_previews,
            docker::docker_probe,
            docker::launch_docker_desktop,
            docker::stack_up,
            docker::stack_down,
            docker::stack_update,
            host::host_connection,
            bootstrap::get_forgejo_viewer_status,
            bootstrap::configure_forgejo_viewer,
            environment::provision_environment_request,
            environment::run_validation_job,
            desktop_services::load_desktop_settings,
            desktop_services::save_desktop_settings,
            desktop_services::confirm_exit,
            desktop_services::cancel_exit,
            desktop_services::notify_attention,
            desktop_services::disk_space_probe,
            desktop_services::create_backup,
            desktop_services::inspect_backup,
            desktop_services::restore_backup,
            updater::check_desktop_update,
            updater::install_desktop_update,
            updater::restart_app,
            store::load_ui_state,
            store::save_ui_state,
            import::inspect_folder,
            import::import_project,
            import::get_project_source,
            import::delete_project,
            git_providers::probe_git_providers,
            git_providers::create_project_remote,
            handoff::synchronize_feature_locally,
            handoff::plain_folder::synchronize_feature_to_folder,
            handoff::project::get_project_sync_state,
            handoff::project::get_git_identity,
            handoff::project::initialize_project_local_repository,
            handoff::project::synchronize_project_locally,
            handoff::project::prepare_local_changes,
            handoff::project::record_local_changes_brought_in,
            handoff::project::preview_project_upstream_branch,
            handoff::project::publish_project_upstream_branch,
            handoff::upstream::preview_upstream_branch,
            handoff::upstream::publish_upstream_branch,
            preview::start_preview,
            preview::stop_preview,
            preview::reset_preview_data,
            preview::get_preview_status,
            preview::get_preview_logs,
            preview_check::run_preview_check,
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
        .run(move |app, event| match event {
            tauri::RunEvent::ExitRequested { api, .. }
                if desktop_services::exit_behavior(app)
                    == desktop_services::ExitBehavior::StopStack
                    && !updater::is_restarting(app) =>
            {
                let coordinator = app.state::<desktop_services::ExitCoordinator>();
                match desktop_services::begin_exit_stop(&coordinator) {
                    desktop_services::ExitRequestAction::Prompt(request_id) => {
                        api.prevent_exit();
                        let _ = app.emit(
                            desktop_services::EXIT_CONFIRMATION_EVENT,
                            desktop_services::ExitConfirmationRequested::new(),
                        );
                        desktop_services::schedule_exit_timeout(
                            app.clone(),
                            shutdown_profiles.clone(),
                            request_id,
                        );
                    }
                    desktop_services::ExitRequestAction::Wait => api.prevent_exit(),
                    desktop_services::ExitRequestAction::Allow => {}
                }
            }
            tauri::RunEvent::Exit | tauri::RunEvent::ExitRequested { .. } => {
                preview::stop_all(&shutdown_previews);
                shutdown_profiles.shutdown();
                app.state::<host::Host>().stop();
            }
            _ => {}
        });
}
