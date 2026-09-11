//! Trusted host bootstrap for Commitarium-owned credentials.
//!
//! The frontend never sees this module. It creates internal transport tokens
//! before Compose evaluates bind mounts, then uses Forgejo's fixed admin CLI to
//! create the service/agent identities and their scoped tokens. Existing valid
//! files and users are adopted so repeated app starts are harmless.

use getrandom::fill as random_fill;
use std::collections::HashSet;
use std::fs::{self, OpenOptions};
use std::io::Write;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::thread;
use std::time::Duration;
use zeroize::Zeroizing;

const FORGEJO_READY_ATTEMPTS: usize = 60;
const FORGEJO_READY_DELAY: Duration = Duration::from_millis(250);

struct SecretLocation {
    source_variable: &'static str,
    relative_path: &'static str,
}

const TRANSPORT_SECRETS: &[SecretLocation] = &[
    SecretLocation {
        source_variable: "COMMITARIUM_SIMULATED_WORKER_TOKEN_SOURCE",
        relative_path: ".commitarium/internal/simulated-worker-token",
    },
    SecretLocation {
        source_variable: "COMMITARIUM_CODEX_WORKER_TOKEN_SOURCE",
        relative_path: ".commitarium/internal/codex-lead-worker-token",
    },
    SecretLocation {
        source_variable: "COMMITARIUM_CODEX_REVIEWER_WORKER_TOKEN_SOURCE",
        relative_path: ".commitarium/internal/codex-reviewer-worker-token",
    },
    SecretLocation {
        source_variable: "COMMITARIUM_CLAUDE_WORKER_TOKEN_SOURCE",
        relative_path: ".commitarium/internal/claude-lead-worker-token",
    },
    SecretLocation {
        source_variable: "COMMITARIUM_CLAUDE_REVIEWER_WORKER_TOKEN_SOURCE",
        relative_path: ".commitarium/internal/claude-reviewer-worker-token",
    },
];

struct ForgejoIdentity {
    username: &'static str,
    email: &'static str,
    admin: bool,
    token: SecretLocation,
    scopes: &'static str,
}

const FORGEJO_IDENTITIES: &[ForgejoIdentity] = &[
    ForgejoIdentity {
        username: "commitarium_admin",
        email: "admin@commitarium.local",
        admin: true,
        token: SecretLocation {
            source_variable: "COMMITARIUM_FORGEJO_TOKEN_SOURCE",
            relative_path: ".commitarium/forgejo-token",
        },
        scopes: "write:user,write:repository,read:issue",
    },
    ForgejoIdentity {
        username: "codex-lead",
        email: "codex-lead@commitarium.local",
        admin: false,
        token: SecretLocation {
            source_variable: "COMMITARIUM_CODEX_FORGEJO_TOKEN_SOURCE",
            relative_path: ".commitarium/agents/codex-lead/forgejo-token",
        },
        scopes: "write:repository,write:issue",
    },
    ForgejoIdentity {
        username: "codex-reviewer",
        email: "codex-reviewer@commitarium.local",
        admin: false,
        token: SecretLocation {
            source_variable: "COMMITARIUM_CODEX_REVIEWER_FORGEJO_TOKEN_SOURCE",
            relative_path: ".commitarium/agents/codex-reviewer/forgejo-token",
        },
        scopes: "write:repository,write:issue",
    },
    ForgejoIdentity {
        username: "claude-lead",
        email: "claude-lead@commitarium.local",
        admin: false,
        token: SecretLocation {
            source_variable: "COMMITARIUM_CLAUDE_FORGEJO_TOKEN_SOURCE",
            relative_path: ".commitarium/agents/claude-lead/forgejo-token",
        },
        scopes: "write:repository,write:issue",
    },
    ForgejoIdentity {
        username: "claude-reviewer",
        email: "claude-reviewer@commitarium.local",
        admin: false,
        token: SecretLocation {
            source_variable: "COMMITARIUM_CLAUDE_REVIEWER_FORGEJO_TOKEN_SOURCE",
            relative_path: ".commitarium/agents/claude-reviewer/forgejo-token",
        },
        scopes: "write:repository,write:issue",
    },
];

fn compose_root(compose_file: &Path) -> Result<&Path, String> {
    compose_file
        .parent()
        .ok_or_else(|| "Compose file has no parent directory".to_string())
}

fn secret_path(root: &Path, location: &SecretLocation) -> PathBuf {
    std::env::var(location.source_variable)
        .ok()
        .filter(|value| !value.trim().is_empty())
        .map(PathBuf::from)
        .unwrap_or_else(|| root.join(location.relative_path))
}

/// Ensure all coordinator-to-worker bearer tokens exist before Compose can
/// turn a missing bind-mount source into a directory. The result says whether
/// any source changed, so the launcher can recreate containers whose old mount
/// was created while that source had the wrong type.
pub(crate) fn prepare_transport_secrets(compose_file: &Path) -> Result<bool, String> {
    let root = compose_root(compose_file)?;
    let mut changed = false;
    for location in TRANSPORT_SECRETS {
        let path = secret_path(root, location);
        if secret_is_ready(&path)? {
            continue;
        }
        let mut bytes = Zeroizing::new(vec![0_u8; 32]);
        random_fill(bytes.as_mut_slice())
            .map_err(|_| "secure randomness is unavailable".to_string())?;
        let mut token = Zeroizing::new(String::with_capacity(bytes.len() * 2));
        for byte in bytes.iter() {
            use std::fmt::Write as _;
            write!(&mut *token, "{byte:02x}")
                .map_err(|_| "could not encode an internal credential".to_string())?;
        }
        write_secret(&path, token.as_bytes())?;
        changed = true;
    }
    Ok(changed)
}

/// Create any missing Forgejo identities and scoped credentials after Forgejo
/// itself is running. Credential values are written directly to private files
/// and are never returned across Tauri IPC.
pub(crate) fn provision_forgejo(compose_file: &Path, project_name: &str) -> Result<bool, String> {
    let root = compose_root(compose_file)?;
    let mut admin = DockerForgejoAdmin::new(compose_file, project_name);
    let users = wait_for_users(&mut admin)?;
    ensure_forgejo_credentials(root, &mut admin, users)
}

trait ForgejoAdmin {
    fn list_users(&mut self) -> Result<HashSet<String>, String>;
    fn create_user(&mut self, identity: &ForgejoIdentity) -> Result<(), String>;
    fn generate_token(
        &mut self,
        username: &str,
        scopes: &str,
    ) -> Result<Zeroizing<Vec<u8>>, String>;
}

fn wait_for_users(admin: &mut impl ForgejoAdmin) -> Result<HashSet<String>, String> {
    let mut last_error = "Forgejo is not ready".to_string();
    for _ in 0..FORGEJO_READY_ATTEMPTS {
        match admin.list_users() {
            Ok(users) => return Ok(users),
            Err(error) => last_error = error,
        }
        thread::sleep(FORGEJO_READY_DELAY);
    }
    Err(last_error)
}

fn ensure_forgejo_credentials(
    root: &Path,
    admin: &mut impl ForgejoAdmin,
    mut users: HashSet<String>,
) -> Result<bool, String> {
    let mut changed = false;
    for identity in FORGEJO_IDENTITIES {
        let created = if users.contains(identity.username) {
            false
        } else {
            admin.create_user(identity)?;
            users.insert(identity.username.to_string());
            true
        };
        let path = secret_path(root, &identity.token);
        if !created && secret_is_ready(&path)? {
            continue;
        }
        let token = admin.generate_token(identity.username, identity.scopes)?;
        validate_secret_bytes(&token)?;
        write_secret(&path, &token)?;
        changed = true;
    }
    Ok(changed)
}

struct DockerForgejoAdmin {
    compose_file: PathBuf,
    project_name: String,
}

impl DockerForgejoAdmin {
    fn new(compose_file: &Path, project_name: &str) -> Self {
        Self {
            compose_file: compose_file.to_path_buf(),
            project_name: project_name.to_string(),
        }
    }

    fn command(&self) -> Command {
        let mut command = Command::new("docker");
        command.args(["compose", "-f"]);
        command.arg(&self.compose_file);
        command.args(["-p", &self.project_name, "exec", "-T", "forgejo", "forgejo"]);
        command
    }
}

impl ForgejoAdmin for DockerForgejoAdmin {
    fn list_users(&mut self) -> Result<HashSet<String>, String> {
        let output = self
            .command()
            .args(["admin", "user", "list"])
            .output()
            .map_err(|_| "could not inspect Forgejo users".to_string())?;
        if !output.status.success() {
            return Err("Forgejo is not ready for identity setup".to_string());
        }
        let stdout = String::from_utf8(output.stdout)
            .map_err(|_| "Forgejo returned invalid user-list output".to_string())?;
        Ok(parse_usernames(&stdout))
    }

    fn create_user(&mut self, identity: &ForgejoIdentity) -> Result<(), String> {
        let mut command = self.command();
        command.args([
            "admin",
            "user",
            "create",
            "--username",
            identity.username,
            "--email",
            identity.email,
            "--random-password",
            "--must-change-password=false",
        ]);
        if identity.admin {
            command.arg("--admin");
        } else {
            command.arg("--restricted");
        }
        let status = command
            .stdout(Stdio::null())
            .stderr(Stdio::null())
            .status()
            .map_err(|_| format!("could not create Forgejo identity {}", identity.username))?;
        if !status.success() {
            return Err(format!(
                "could not create Forgejo identity {}",
                identity.username
            ));
        }
        Ok(())
    }

    fn generate_token(
        &mut self,
        username: &str,
        scopes: &str,
    ) -> Result<Zeroizing<Vec<u8>>, String> {
        let token_name = format!("commitarium-bootstrap-{}", random_hex(8)?);
        let output = self
            .command()
            .args([
                "admin",
                "user",
                "generate-access-token",
                "--username",
                username,
                "--token-name",
                &token_name,
                "--scopes",
                scopes,
                "--raw",
            ])
            .output()
            .map_err(|_| format!("could not create Forgejo token for {username}"))?;
        let status = output.status;
        let stdout = Zeroizing::new(output.stdout);
        let _stderr = Zeroizing::new(output.stderr);
        if !status.success() {
            return Err(format!("could not create Forgejo token for {username}"));
        }
        Ok(stdout)
    }
}

fn parse_usernames(output: &str) -> HashSet<String> {
    output
        .lines()
        .filter_map(|line| {
            let columns: Vec<_> = line.split_whitespace().collect();
            if columns.len() >= 2 && columns[0].parse::<u64>().is_ok() {
                Some(columns[1].to_string())
            } else {
                None
            }
        })
        .collect()
}

fn random_hex(byte_count: usize) -> Result<String, String> {
    let mut bytes = Zeroizing::new(vec![0_u8; byte_count]);
    random_fill(bytes.as_mut_slice())
        .map_err(|_| "secure randomness is unavailable".to_string())?;
    let mut result = String::with_capacity(byte_count * 2);
    for byte in bytes.iter() {
        use std::fmt::Write as _;
        write!(&mut result, "{byte:02x}")
            .map_err(|_| "could not encode a credential identifier".to_string())?;
    }
    Ok(result)
}

fn secret_is_ready(path: &Path) -> Result<bool, String> {
    let metadata = match fs::symlink_metadata(path) {
        Ok(metadata) => metadata,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(false),
        Err(_) => return Err(format!("could not inspect private file {}", path.display())),
    };
    if metadata.file_type().is_symlink() {
        return Err(format!(
            "private file {} must not be a symlink",
            path.display()
        ));
    }
    if metadata.is_dir() {
        fs::remove_dir(path).map_err(|_| {
            format!(
                "private file {} is a non-empty directory; move its contents and retry",
                path.display()
            )
        })?;
        return Ok(false);
    }
    if !metadata.is_file() {
        return Err(format!("private path {} is not a file", path.display()));
    }
    let bytes = fs::read(path)
        .map(Zeroizing::new)
        .map_err(|_| format!("could not read private file {}", path.display()))?;
    if bytes.iter().all(u8::is_ascii_whitespace) {
        return Ok(false);
    }
    validate_secret_bytes(&bytes)?;
    set_private_file_permissions(path)?;
    Ok(true)
}

fn validate_secret_bytes(bytes: &[u8]) -> Result<(), String> {
    let trimmed = trim_ascii_whitespace(bytes);
    if trimmed.is_empty() || trimmed.iter().any(u8::is_ascii_whitespace) {
        return Err("a generated private credential had an invalid format".to_string());
    }
    Ok(())
}

fn trim_ascii_whitespace(mut bytes: &[u8]) -> &[u8] {
    while bytes.first().is_some_and(u8::is_ascii_whitespace) {
        bytes = &bytes[1..];
    }
    while bytes.last().is_some_and(u8::is_ascii_whitespace) {
        bytes = &bytes[..bytes.len() - 1];
    }
    bytes
}

fn write_secret(path: &Path, secret: &[u8]) -> Result<(), String> {
    validate_secret_bytes(secret)?;
    let parent = path
        .parent()
        .ok_or_else(|| format!("private file {} has no parent directory", path.display()))?;
    fs::create_dir_all(parent)
        .map_err(|_| format!("could not create private directory {}", parent.display()))?;
    set_private_directory_permissions(parent)?;

    let temporary = parent.join(format!(".commitarium-secret-{}", random_hex(8)?));
    let result = (|| {
        let mut file = OpenOptions::new()
            .write(true)
            .create_new(true)
            .open(&temporary)
            .map_err(|_| "could not create temporary private file".to_string())?;
        set_private_file_permissions(&temporary)?;
        file.write_all(trim_ascii_whitespace(secret))
            .map_err(|_| "could not write private credential".to_string())?;
        file.sync_all()
            .map_err(|_| "could not persist private credential".to_string())?;
        drop(file);
        match fs::symlink_metadata(path) {
            Ok(metadata) if metadata.is_dir() => fs::remove_dir(path)
                .map_err(|_| format!("private file {} is a non-empty directory", path.display()))?,
            Ok(metadata) if metadata.is_file() => fs::remove_file(path)
                .map_err(|_| format!("could not replace private file {}", path.display()))?,
            Ok(_) => {
                return Err(format!(
                    "private path {} cannot be replaced",
                    path.display()
                ))
            }
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {}
            Err(_) => return Err(format!("could not inspect private file {}", path.display())),
        }
        fs::rename(&temporary, path)
            .map_err(|_| format!("could not install private file {}", path.display()))?;
        set_private_file_permissions(path)
    })();
    if result.is_err() {
        let _ = fs::remove_file(&temporary);
    }
    result
}

#[cfg(unix)]
fn set_private_directory_permissions(path: &Path) -> Result<(), String> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(0o700))
        .map_err(|_| format!("could not protect private directory {}", path.display()))
}

#[cfg(not(unix))]
fn set_private_directory_permissions(_path: &Path) -> Result<(), String> {
    Ok(())
}

#[cfg(unix)]
fn set_private_file_permissions(path: &Path) -> Result<(), String> {
    use std::os::unix::fs::PermissionsExt;
    fs::set_permissions(path, fs::Permissions::from_mode(0o600))
        .map_err(|_| format!("could not protect private file {}", path.display()))
}

#[cfg(not(unix))]
fn set_private_file_permissions(_path: &Path) -> Result<(), String> {
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    #[derive(Default)]
    struct FakeAdmin {
        users: HashSet<String>,
        created: Vec<String>,
        generated: Vec<(String, String)>,
        tokens: HashMap<String, Vec<u8>>,
    }

    impl ForgejoAdmin for FakeAdmin {
        fn list_users(&mut self) -> Result<HashSet<String>, String> {
            Ok(self.users.clone())
        }

        fn create_user(&mut self, identity: &ForgejoIdentity) -> Result<(), String> {
            self.created.push(identity.username.to_string());
            self.users.insert(identity.username.to_string());
            Ok(())
        }

        fn generate_token(
            &mut self,
            username: &str,
            scopes: &str,
        ) -> Result<Zeroizing<Vec<u8>>, String> {
            self.generated
                .push((username.to_string(), scopes.to_string()));
            Ok(Zeroizing::new(
                self.tokens
                    .get(username)
                    .cloned()
                    .unwrap_or_else(|| format!("token-{username}").into_bytes()),
            ))
        }
    }

    #[test]
    fn transport_bootstrap_creates_and_preserves_private_tokens() {
        let root = tempfile::tempdir().expect("temp root");
        let compose_file = root.path().join("compose.yml");
        fs::write(&compose_file, "services: {}").expect("compose file");

        assert!(prepare_transport_secrets(&compose_file).expect("first bootstrap"));
        let first: Vec<Vec<u8>> = TRANSPORT_SECRETS
            .iter()
            .map(|location| fs::read(secret_path(root.path(), location)).expect("token"))
            .collect();
        assert!(!prepare_transport_secrets(&compose_file).expect("second bootstrap"));

        for (index, location) in TRANSPORT_SECRETS.iter().enumerate() {
            let path = secret_path(root.path(), location);
            let second = fs::read(&path).expect("preserved token");
            assert_eq!(first[index], second);
            assert_eq!(second.len(), 64);
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                assert_eq!(
                    fs::metadata(path).expect("metadata").permissions().mode() & 0o777,
                    0o600
                );
            }
        }
    }

    #[test]
    fn forgejo_bootstrap_adopts_users_repairs_empty_mount_directories_and_is_idempotent() {
        let root = tempfile::tempdir().expect("temp root");
        let existing = &FORGEJO_IDENTITIES[0];
        let existing_path = secret_path(root.path(), &existing.token);
        fs::create_dir_all(existing_path.parent().expect("parent")).expect("parent");
        fs::write(&existing_path, b"existing-token").expect("existing token");

        let broken = &FORGEJO_IDENTITIES[1];
        fs::create_dir_all(secret_path(root.path(), &broken.token)).expect("empty mount directory");

        let mut admin = FakeAdmin::default();
        admin.users.insert(existing.username.to_string());
        let users = admin.users.clone();
        assert!(
            ensure_forgejo_credentials(root.path(), &mut admin, users).expect("first bootstrap")
        );

        assert_eq!(
            fs::read(existing_path).expect("existing token"),
            b"existing-token"
        );
        assert_eq!(admin.created.len(), FORGEJO_IDENTITIES.len() - 1);
        assert_eq!(admin.generated.len(), FORGEJO_IDENTITIES.len() - 1);
        assert!(admin
            .generated
            .iter()
            .all(|(_, scopes)| !scopes.contains("all")));
        assert!(secret_path(root.path(), &broken.token).is_file());

        let created = admin.created.len();
        let generated = admin.generated.len();
        let users = admin.users.clone();
        assert!(
            !ensure_forgejo_credentials(root.path(), &mut admin, users).expect("second bootstrap")
        );
        assert_eq!(admin.created.len(), created);
        assert_eq!(admin.generated.len(), generated);
    }

    #[test]
    fn username_parser_ignores_headers_and_noise() {
        let parsed = parse_usernames(
            "ID Username Email IsActive\n1 commitarium_admin admin@example.test true\nwarning\n2 claude-lead claude@example.test true\n",
        );
        assert_eq!(
            parsed,
            HashSet::from(["commitarium_admin".to_string(), "claude-lead".to_string()])
        );
    }

    #[test]
    fn non_empty_directory_is_never_destroyed_as_a_secret_file_repair() {
        let root = tempfile::tempdir().expect("temp root");
        let path = root.path().join("token");
        fs::create_dir(&path).expect("directory");
        fs::write(path.join("keep"), b"user data").expect("user data");
        let error = secret_is_ready(&path).expect_err("non-empty directory must fail");
        assert!(error.contains("non-empty directory"));
        assert!(path.join("keep").exists());
    }
}
