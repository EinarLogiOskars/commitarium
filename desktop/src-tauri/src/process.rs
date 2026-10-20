//! Every command-line program the desktop starts (docker, git, curl, …) goes
//! through `command`. On Windows a windowed app's child processes would each
//! open a console window, so the desktop flashed and left behind a terminal per
//! command; `CREATE_NO_WINDOW` keeps them hidden. Elsewhere it changes nothing.

use std::ffi::OsStr;
use std::process::Command;

/// A command for `program` that opens no console window.
pub(crate) fn command(program: impl AsRef<OsStr>) -> Command {
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

#[cfg(test)]
mod tests {
    use std::fs;
    use std::path::Path;

    fn sources(dir: &Path, found: &mut Vec<(String, String)>) {
        for entry in fs::read_dir(dir).unwrap() {
            let path = entry.unwrap().path();
            if path.is_dir() {
                sources(&path, found);
            } else if path.extension().is_some_and(|ext| ext == "rs") {
                found.push((
                    path.display().to_string(),
                    fs::read_to_string(&path).unwrap(),
                ));
            }
        }
    }

    /// A program started any other way would open a console window on
    /// Windows again.
    #[test]
    fn every_program_starts_through_command() {
        let mut found = Vec::new();
        sources(
            &Path::new(env!("CARGO_MANIFEST_DIR")).join("src"),
            &mut found,
        );
        let offenders: Vec<_> = found
            .iter()
            .filter(|(path, text)| !path.ends_with("process.rs") && text.contains("Command::new("))
            .map(|(path, _)| path.as_str())
            .collect();
        assert!(
            offenders.is_empty(),
            "start programs with process::command: {offenders:?}"
        );
    }
}
