//! Starts the jobs nobody clicks for: validation of work orders that merge on
//! their own, Commitarium's acceptance-test runs, and preview checks. Each is
//! started once per host run; a job's own claim keeps a manual run from racing
//! it, and an unreachable coordinator just means nothing to start yet.

use std::collections::HashSet;
use std::sync::Arc;
use std::thread;
use std::time::Duration;

use serde::Deserialize;

use crate::preview::{coordinator_json, coordinator_url};
use crate::runtime::block_on;
use crate::server::Shared;
use crate::{environment, preview_check};

const POLL_EVERY: Duration = Duration::from_secs(10);

#[derive(Deserialize)]
struct Attention {
    items: Vec<AttentionItem>,
}

#[derive(Deserialize)]
struct AttentionItem {
    kind: String,
    #[serde(default)]
    auto_run: bool,
    validation_job_id: Option<String>,
}

#[derive(Deserialize)]
struct AcceptanceJobs {
    jobs: Vec<Pending>,
}

#[derive(Deserialize)]
struct PreviewChecks {
    checks: Vec<Pending>,
}

#[derive(Deserialize)]
struct Pending {
    id: String,
}

/// Watches for jobs until the process ends.
pub fn watch(shared: Arc<Shared>) {
    thread::spawn(move || {
        let client = reqwest::Client::new();
        let mut started = Started::default();
        loop {
            block_on(start_pending(&client, &shared, &mut started));
            thread::sleep(POLL_EVERY);
        }
    });
}

#[derive(Default)]
struct Started {
    validation: HashSet<String>,
    preview_checks: HashSet<String>,
}

async fn start_pending(client: &reqwest::Client, shared: &Arc<Shared>, started: &mut Started) {
    let mut validation = Vec::new();
    if let Ok(attention) = pending::<Attention>(client, "/api/v1/attention", "attention").await {
        validation.extend(auto_validation(attention));
    }
    if let Ok(acceptance) =
        pending::<AcceptanceJobs>(client, "/api/v1/acceptance-jobs/pending", "acceptance jobs")
            .await
    {
        validation.extend(acceptance.jobs.into_iter().map(|job| job.id));
    }
    for job_id in validation {
        if started.validation.insert(job_id.clone()) {
            thread::spawn(move || block_on(environment::run_validation_job(job_id)));
        }
    }

    let Ok(checks) =
        pending::<PreviewChecks>(client, "/api/v1/preview-checks/pending", "preview checks").await
    else {
        return;
    };
    for check in checks.checks {
        if started.preview_checks.insert(check.id.clone()) {
            let shared = shared.clone();
            thread::spawn(move || {
                shared.wait_for_sweep();
                block_on(preview_check::run_preview_check(check.id))
            });
        }
    }
}

async fn pending<T: for<'de> Deserialize<'de>>(
    client: &reqwest::Client,
    path: &str,
    name: &str,
) -> Result<T, String> {
    let response = client
        .get(coordinator_url(path))
        .send()
        .await
        .map_err(|e| format!("{name}: {e}"))?;
    coordinator_json(response, name).await
}

/// The validation jobs of work orders that merge once their gates pass.
fn auto_validation(attention: Attention) -> impl Iterator<Item = String> {
    attention.items.into_iter().filter_map(|item| {
        (item.kind == "validation_pending" && item.auto_run)
            .then_some(item.validation_job_id)
            .flatten()
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_auto_run_validation_jobs_start_by_themselves() {
        let attention: Attention = serde_json::from_str(
            r#"{"items": [
                {"kind": "validation_pending", "auto_run": true, "validation_job_id": "val_auto"},
                {"kind": "validation_pending", "validation_job_id": "val_manual"},
                {"kind": "validation_pending", "auto_run": true},
                {"kind": "merge_ready", "auto_run": true, "validation_job_id": "val_other"}
            ]}"#,
        )
        .unwrap();

        assert_eq!(auto_validation(attention).collect::<Vec<_>>(), ["val_auto"]);
    }
}
