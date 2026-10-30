//! The desktop starts programs the host's way (`commitarium_host::process`).

pub(crate) use commitarium_host::process::command;

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
