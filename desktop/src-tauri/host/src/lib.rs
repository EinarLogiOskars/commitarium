//! Commitarium's trusted host: the work beside the Docker stack, split out of
//! the desktop app so it can also run without one (ADR-040).

pub mod disk;
pub mod docker;
pub mod events;
pub mod process;
pub mod server;
