//! Deployments (PUBLISHING_PLAN.md): each app's frozen copy of one commit,
//! apart from its preview, with data of its own. What is deployed is kept in
//! a file, so a host that starts again deploys the same versions again.

use crate::events::Events;
use crate::preview::{self, PreviewManager, PreviewStatus, PreviewTarget};
use serde::{Deserialize, Serialize};
use std::collections::BTreeMap;
use std::fs;
use std::io::ErrorKind;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex};
use std::time::{SystemTime, UNIX_EPOCH};

const FILE: &str = "deployments.json";
/// Enough to roll back a good way, small enough to read at a glance.
const HISTORY: usize = 20;

/// One deploy: which commit, and when.
#[derive(Clone, Debug, Deserialize, PartialEq, Serialize)]
pub struct Deploy {
    pub commit_id: String,
    /// Seconds since the Unix epoch.
    pub deployed_at: u64,
}

#[derive(Clone, Debug, Default, Deserialize, Serialize)]
struct Record {
    /// What should be running: `None` once stopped.
    current: Option<Deploy>,
    /// Every deploy, newest first.
    history: Vec<Deploy>,
}

impl Record {
    fn deployed(&mut self, deploy: Deploy) {
        self.history.insert(0, deploy.clone());
        self.history.truncate(HISTORY);
        self.current = Some(deploy);
    }
}

/// A project's deployment as a client sees it.
#[derive(Serialize)]
pub struct Deployment {
    project_id: String,
    current: Option<Deploy>,
    history: Vec<Deploy>,
    /// The running copy's state, while one runs or starts.
    status: Option<PreviewStatus>,
}

#[derive(Clone)]
pub struct Deployments {
    manager: PreviewManager,
    file: PathBuf,
    /// Orders changes to the file.
    records: Arc<Mutex<()>>,
}

impl Deployments {
    pub fn new(data_dir: &Path) -> Self {
        Self {
            manager: PreviewManager::default(),
            file: data_dir.join(FILE),
            records: Arc::default(),
        }
    }

    /// Whether `port` is the local port of a running deployment.
    pub fn serves_port(&self, port: u16) -> bool {
        self.manager.serves_port(port)
    }

    fn read(&self) -> Result<BTreeMap<String, Record>, String> {
        match fs::read(&self.file) {
            Ok(body) => serde_json::from_slice(&body)
                .map_err(|_| format!("{} is not valid", self.file.display())),
            Err(error) if error.kind() == ErrorKind::NotFound => Ok(BTreeMap::new()),
            Err(error) => Err(format!("read {}: {error}", self.file.display())),
        }
    }

    fn write(&self, records: &BTreeMap<String, Record>) -> Result<(), String> {
        let body = serde_json::to_vec_pretty(records).map_err(|e| e.to_string())?;
        let temporary = self.file.with_extension("json.new");
        fs::write(&temporary, body)
            .and_then(|()| fs::rename(&temporary, &self.file))
            .map_err(|e| format!("save the deployments: {e}"))
    }

    fn change(&self, project_id: &str, change: impl FnOnce(&mut Record)) -> Result<(), String> {
        let _held = self
            .records
            .lock()
            .map_err(|_| "deployments are unavailable")?;
        let mut records = self.read()?;
        change(records.entry(project_id.to_string()).or_default());
        self.write(&records)
    }

    /// Deploys `commit_id`, or the default branch's head. The running copy
    /// is replaced in place, keeping its data.
    pub async fn deploy(
        &self,
        events: Events,
        project_id: String,
        commit_id: Option<String>,
    ) -> Result<PreviewStatus, String> {
        let status = preview::start_preview(
            events,
            &self.manager,
            project_id.clone(),
            PreviewTarget::Deployment { commit_id },
        )
        .await?;
        let deploy = Deploy {
            commit_id: status.commit_id().to_string(),
            deployed_at: now(),
        };
        self.change(&project_id, |record| record.deployed(deploy))?;
        Ok(status)
    }

    /// Stops a deployment. Its data stays for the next deploy.
    pub async fn stop(&self, events: Events, project_id: String) -> Result<(), String> {
        preview::stop_deployment(events, &self.manager, project_id.clone()).await?;
        self.change(&project_id, |record| record.current = None)
    }

    pub fn list(&self) -> Result<Vec<Deployment>, String> {
        let records = self.read()?;
        records
            .into_iter()
            .map(|(project_id, record)| {
                Ok(Deployment {
                    status: preview::deployment_status(&self.manager, &project_id)?,
                    project_id,
                    current: record.current,
                    history: record.history,
                })
            })
            .collect()
    }

    /// Starts again what was deployed, for a host that has just started.
    pub async fn restore(&self, events: Events) {
        let Ok(records) = self.read() else {
            return;
        };
        for (project_id, record) in records {
            let Some(current) = record.current else {
                continue;
            };
            let started = preview::start_preview(
                events.clone(),
                &self.manager,
                project_id.clone(),
                PreviewTarget::Deployment {
                    commit_id: Some(current.commit_id),
                },
            )
            .await;
            if let Err(error) = started {
                eprintln!("[deployments] {project_id} did not start again: {error}");
            }
        }
    }

    /// Stops every running copy, for a host that is leaving. What is
    /// deployed stays recorded, so the next host starts it again.
    pub fn stop_all(&self) {
        preview::stop_all(&self.manager);
    }
}

fn now() -> u64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|elapsed| elapsed.as_secs())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn deploy(commit: &str, at: u64) -> Deploy {
        Deploy {
            commit_id: commit.into(),
            deployed_at: at,
        }
    }

    #[test]
    fn a_deploy_becomes_current_and_heads_the_history() {
        let mut record = Record::default();
        record.deployed(deploy("a", 1));
        record.deployed(deploy("b", 2));
        assert_eq!(record.current, Some(deploy("b", 2)));
        assert_eq!(record.history, vec![deploy("b", 2), deploy("a", 1)]);
    }

    #[test]
    fn the_history_keeps_the_newest() {
        let mut record = Record::default();
        for at in 0..(HISTORY as u64 + 5) {
            record.deployed(deploy("c", at));
        }
        assert_eq!(record.history.len(), HISTORY);
        assert_eq!(record.history[0].deployed_at, HISTORY as u64 + 4);
    }

    #[test]
    fn records_are_kept_in_the_data_directory() {
        let dir = tempfile::tempdir().unwrap();
        let deployments = Deployments::new(dir.path());
        deployments
            .change("prj_one", |record| record.deployed(deploy("abc", 5)))
            .unwrap();
        deployments
            .change("prj_one", |record| record.current = None)
            .unwrap();
        let again = Deployments::new(dir.path());
        let records = again.read().unwrap();
        assert_eq!(records["prj_one"].current, None);
        assert_eq!(records["prj_one"].history, vec![deploy("abc", 5)]);
    }
}
