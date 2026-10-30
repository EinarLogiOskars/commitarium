//! Every command-line program Commitarium starts (docker, git, curl, …) goes
//! through `command`. On Windows a windowed app's child processes would each
//! open a console window, so the desktop flashed and left behind a terminal per
//! command; `CREATE_NO_WINDOW` keeps them hidden. Elsewhere it changes nothing.

use std::ffi::OsStr;
use std::process::Command;

/// A command for `program` that opens no console window.
pub fn command(program: impl AsRef<OsStr>) -> Command {
    let mut command = Command::new(program);
    hide_console(&mut command);
    command
}

#[cfg(windows)]
fn hide_console(command: &mut Command) {
    use std::os::windows::process::CommandExt;
    /// The Windows process creation flag for a console process without a
    /// console window.
    const CREATE_NO_WINDOW: u32 = 0x0800_0000;
    command.creation_flags(CREATE_NO_WINDOW);
}

#[cfg(not(windows))]
fn hide_console(_command: &mut Command) {}
