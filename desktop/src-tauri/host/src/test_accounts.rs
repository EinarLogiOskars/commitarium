//! A project's test logins for its preview (HELPERS_PLAN.md, H3).
//!
//! The coordinator keeps them; the host reads them when it starts a preview
//! (never a deployment), writes them into the preview's own directory, and
//! the Compose rewrite mounts that file read-only into every container at
//! `preview_compose::TEST_ACCOUNTS_TARGET`. The app seeds them itself. The
//! list holds passwords, so nothing here logs it or quotes it in an error.

use serde::{Deserialize, Serialize};
use std::fs;
use std::io::Write;
use std::path::{Path, PathBuf};

/// Every test login's domain; the app removes logins at it that are no
/// longer listed.
const DOMAIN: &str = "commitarium.test";
/// More than the coordinator keeps, to bound what is read.
const MAX_ACCOUNTS: usize = 100;
const MAX_FIELD: usize = 400;
const FILE_NAME: &str = "test-accounts.json";

/// The file the preview's app reads. Deliberately not `Debug`, so it cannot
/// end up in a log line.
#[derive(Deserialize, Serialize)]
pub(crate) struct TestAccounts {
    accounts: Vec<TestAccount>,
    domain: String,
}

#[derive(Deserialize, Serialize)]
struct TestAccount {
    username: String,
    password: String,
    role: String,
    label: String,
}

impl TestAccounts {
    fn validate(&self) -> Result<(), String> {
        let suffix = format!("@{DOMAIN}");
        let valid = self.domain == DOMAIN
            && self.accounts.len() <= MAX_ACCOUNTS
            && self.accounts.iter().all(|account| {
                account.username.ends_with(&suffix)
                    && !account.password.is_empty()
                    && [
                        &account.username,
                        &account.password,
                        &account.role,
                        &account.label,
                    ]
                    .iter()
                    .all(|field| field.len() <= MAX_FIELD && !field.chars().any(char::is_control))
            });
        valid
            .then_some(())
            .ok_or_else(|| "the coordinator answered test accounts the preview cannot use".into())
    }

    pub(crate) fn len(&self) -> usize {
        self.accounts.len()
    }
}

/// Reads the project's test logins from the coordinator. A preview without
/// them still starts: it then gets no file, and the app leaves its logins
/// as they are.
pub(crate) async fn fetch(
    client: &reqwest::Client,
    project_id: &str,
) -> Result<TestAccounts, String> {
    let response = client
        .get(crate::preview::coordinator_url(&format!(
            "/api/v1/projects/{project_id}/test-accounts/preview"
        )))
        .send()
        .await
        .map_err(|_| "the coordinator could not be reached for the test accounts".to_string())?;
    if !response.status().is_success() {
        return Err(format!(
            "the coordinator answered HTTP {} for the test accounts",
            response.status()
        ));
    }
    // A decoding error can quote what it read, so it is not passed on.
    let bytes = response
        .bytes()
        .await
        .map_err(|_| "the test accounts could not be read".to_string())?;
    let accounts: TestAccounts = serde_json::from_slice(&bytes)
        .map_err(|_| "the coordinator answered test accounts the preview cannot use".to_string())?;
    accounts.validate()?;
    Ok(accounts)
}

/// Writes the list into the preview's directory and answers its path. The
/// directory is the host user's alone (0700), so the file itself is 0644:
/// the app's containers may run as any user, and a file only its owner can
/// read would stay closed to them.
pub(crate) fn write(accounts: &TestAccounts, directory: &Path) -> Result<PathBuf, String> {
    let contents = serde_json::to_vec(accounts)
        .map_err(|_| "the test accounts could not be written".to_string())?;
    let path = directory.join(FILE_NAME);
    let mut options = fs::OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o644);
    }
    let mut file = options
        .open(&path)
        .map_err(|e| format!("write the preview's test accounts: {e}"))?;
    file.write_all(&contents)
        .map_err(|e| format!("write the preview's test accounts: {e}"))?;
    // The process umask may have narrowed the mode it was created with.
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(&path, fs::Permissions::from_mode(0o644))
            .map_err(|e| format!("write the preview's test accounts: {e}"))?;
    }
    Ok(path)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn parse(text: &str) -> Result<TestAccounts, String> {
        let accounts: TestAccounts = serde_json::from_str(text).map_err(|e| e.to_string())?;
        accounts.validate()?;
        Ok(accounts)
    }

    #[test]
    fn accepts_the_coordinators_file_and_an_empty_list() {
        let accounts = parse(
            r#"{"accounts":[{"username":"admin@commitarium.test","password":"Secret123","role":"admin","label":"Admin"}],"domain":"commitarium.test"}"#,
        )
        .unwrap();
        assert_eq!(accounts.len(), 1);
        assert_eq!(
            parse(r#"{"accounts":[],"domain":"commitarium.test"}"#)
                .unwrap()
                .len(),
            0
        );
    }

    #[test]
    fn rejects_other_domains_and_odd_fields_without_quoting_them() {
        for text in [
            r#"{"accounts":[],"domain":"example.com"}"#,
            r#"{"accounts":[{"username":"real@example.com","password":"p","role":"","label":"A"}],"domain":"commitarium.test"}"#,
            r#"{"accounts":[{"username":"a@commitarium.test","password":"","role":"","label":"A"}],"domain":"commitarium.test"}"#,
            r#"{"accounts":[{"username":"a@commitarium.test","password":"line\nbreak","role":"","label":"A"}],"domain":"commitarium.test"}"#,
        ] {
            let error = parse(text).err().unwrap();
            assert!(
                !error.contains("break") && !error.contains("example.com"),
                "{error}"
            );
        }
    }

    #[test]
    fn the_file_keeps_the_shape_the_app_reads() {
        let directory = tempfile::tempdir().unwrap();
        let accounts = parse(
            r#"{"accounts":[{"username":"admin@commitarium.test","password":"Secret123","role":"admin","label":"Admin"}],"domain":"commitarium.test"}"#,
        )
        .unwrap();
        let path = write(&accounts, directory.path()).unwrap();
        assert_eq!(path, directory.path().join("test-accounts.json"));
        let written: serde_json::Value = serde_json::from_slice(&fs::read(&path).unwrap()).unwrap();
        assert_eq!(written["domain"], "commitarium.test");
        assert_eq!(written["accounts"][0]["username"], "admin@commitarium.test");
        assert_eq!(written["accounts"][0]["password"], "Secret123");
        assert_eq!(written["accounts"][0]["role"], "admin");
        assert_eq!(written["accounts"][0]["label"], "Admin");
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = fs::metadata(&path).unwrap().permissions().mode() & 0o777;
            assert_eq!(mode, 0o644);
        }
    }
}
