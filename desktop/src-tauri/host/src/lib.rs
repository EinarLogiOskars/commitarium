//! Commitarium's trusted host: the work beside the Docker stack, split out of
//! the desktop app so it can also run without one (ADR-040).

pub mod agents;
pub mod bootstrap;
pub mod cleanup;
pub mod cli_updates;
pub mod disk;
pub mod docker;
pub mod environment;
pub mod events;
pub mod jobs;
pub mod preview;
pub mod preview_check;
pub mod preview_compose;
pub mod process;
pub mod profiles;
pub mod runtime;
pub mod server;
