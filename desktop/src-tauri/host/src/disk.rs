//! Free space where Commitarium keeps its data.

use fs2::available_space;
use serde::Serialize;
use std::fs;
use std::path::Path;

/// What a backup should have free before it starts.
const RECOMMENDED_FREE_BYTES: u64 = 5 * 1024 * 1024 * 1024;

#[derive(Debug, Serialize)]
pub struct DiskSpaceProbe {
    path: String,
    available_bytes: u64,
    recommended_free_bytes: u64,
    backup_ready: bool,
}

pub fn disk_space_probe(data_dir: &Path) -> Result<DiskSpaceProbe, String> {
    fs::create_dir_all(data_dir).map_err(|e| format!("create app data directory: {e}"))?;
    let available =
        available_space(data_dir).map_err(|e| format!("inspect free disk space: {e}"))?;
    Ok(DiskSpaceProbe {
        path: data_dir.to_string_lossy().into_owned(),
        available_bytes: available,
        recommended_free_bytes: RECOMMENDED_FREE_BYTES,
        backup_ready: available >= RECOMMENDED_FREE_BYTES,
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn makes_the_data_directory_and_reports_its_space() {
        let dir = std::env::temp_dir()
            .join(format!("commitarium-disk-{}", std::process::id()))
            .join("data");
        let probe = disk_space_probe(&dir).unwrap();
        assert!(dir.is_dir());
        assert_eq!(probe.path, dir.to_string_lossy());
        assert_eq!(
            probe.backup_ready,
            probe.available_bytes >= RECOMMENDED_FREE_BYTES
        );
        std::fs::remove_dir_all(dir.parent().unwrap()).unwrap();
    }
}
