//! Commitarium's trusted host: the work beside the Docker stack, split out of
//! the desktop app so it can also run without one (ADR-040).

pub mod agents;
pub mod apps_page;
pub mod authentik;
pub mod backup;
pub mod bootstrap;
pub mod bridge;
pub mod cleanup;
pub mod cli_updates;
pub mod deployments;
pub mod device_keys;
pub mod disk;
pub mod docker;
pub mod environment;
pub mod events;
#[cfg(unix)]
pub mod gateway;
#[cfg(unix)]
pub mod host_update;
pub mod jobs;
pub mod preview;
pub mod preview_check;
pub mod preview_compose;
pub mod process;
pub mod profiles;
pub mod publishing;
pub mod runtime;
pub mod server;
pub mod server_backups;
#[cfg(unix)]
pub mod service;
pub mod sign_in;
pub mod tailscale;
