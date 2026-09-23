import { useCallback, useEffect, useState } from "react";
import {
  ensureValidationJob,
  getRun,
  getRunValidationJobs,
  mergeRun,
  retryValidationJob,
} from "../api/runs";
import { getWorkspace } from "../api/features";
import { openExternal, runValidationJob } from "../ipc";
import { ApiError } from "../api/client";
import type { ValidationJob, Workspace } from "../api/types";

const POLL_MS = 2000;

// The merge phase: no agent conversation — the PR's status and the human merge
// gate. Under require_user_approval the merge happens here; under
// auto_after_gates the coordinator merges and this just reports the outcome.
export function MergeView({
  projectId,
  featureId,
  runId,
  state,
  live = true,
  onAdvanced,
}: {
  projectId: string;
  featureId: string;
  runId: string;
  state: string;
  live?: boolean;
  onAdvanced: () => void;
}) {
  const [workspace, setWorkspace] = useState<Workspace | null>(null);
  const [jobs, setJobs] = useState<ValidationJob[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [merging, setMerging] = useState(false);
  const [validating, setValidating] = useState(false);
  const [valError, setValError] = useState<string | null>(null);

  const poll = useCallback(async () => {
    try {
      await getRun(runId); // keep the run warm / surface auth errors
      setWorkspace(await getWorkspace(projectId, featureId));
    } catch (e) {
      if (e instanceof ApiError) setError(`${e.message} (${e.code})`);
      // a missing workspace is not an error worth showing
    }
    try {
      setJobs((await getRunValidationJobs(runId)).jobs);
    } catch {
      /* no jobs yet / not configured */
    }
  }, [runId, projectId, featureId]);

  useEffect(() => {
    void poll();
    const id = setInterval(() => void poll(), POLL_MS);
    return () => clearInterval(id);
  }, [poll]);

  const merge = async () => {
    setMerging(true);
    setError(null);
    try {
      await mergeRun(runId, crypto.randomUUID());
      setConfirm(false);
      onAdvanced();
    } catch (e) {
      if (e instanceof ApiError && e.code === "merge_not_ready") {
        // Validation hasn't passed for this commit. Surface (or create) the job.
        setError("Validation must pass before this can merge.");
        setConfirm(false);
        await ensureValidationJob(runId).catch(() => {});
        await poll();
      } else {
        setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
      }
    } finally {
      setMerging(false);
    }
  };

  // Run a pending job (Tauri claims + executes it), then refresh — an
  // auto_after_gates project may complete the run the moment validation passes.
  const runValidation = async (jobId: string) => {
    setValidating(true);
    setValError(null);
    try {
      await runValidationJob(jobId);
      await poll();
      onAdvanced();
    } catch (e) {
      setValError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    } finally {
      setValidating(false);
    }
  };

  const retryValidation = async (jobId: string) => {
    setValidating(true);
    setValError(null);
    try {
      const next = await retryValidationJob(jobId);
      await runValidationJob(next.id);
      await poll();
      onAdvanced();
    } catch (e) {
      setValError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    } finally {
      setValidating(false);
    }
  };

  const pr = workspace?.pull_request;
  const merge_ = workspace?.merge;
  const merged = merge_?.merged_at;
  const done = merged || state === "completed";
  // Latest validation job (newest first); a non-passing one blocks the merge.
  const latestJob = [...jobs].sort((a, b) => b.created_at.localeCompare(a.created_at))[0] ?? null;
  const validationBlocks = !!latestJob && latestJob.status !== "passed";
  // When the gate is open the PR link sits next to Merge instead of the header,
  // so the user can review the code right at the decision point.
  const gateOpen = !done && live && state === "ready_to_merge";
  const openPr = pr ? (
    <button className="ghost" onClick={() => void openExternal(pr.url)} title={pr.url}>
      Open PR #{pr.number} ↗
    </button>
  ) : null;

  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Merge</h2>
        {!gateOpen && openPr}
      </div>
      {error && <div className="banner banner--error">{error}</div>}

      <dl className="detail">
        <dt>Pull request</dt>
        <dd>{pr ? `#${pr.number}${pr.draft ? " (draft)" : ""}` : "not opened yet"}</dd>
        <dt>Status</dt>
        <dd className={done ? "" : "muted"}>
          {done
            ? "Merged into the default branch ✓"
            : state === "ready_to_merge"
              ? "Ready to merge"
              : "Awaiting review"}
        </dd>
        {merge_?.merge_commit_id && (
          <>
            <dt>Merge commit</dt>
            <dd className="muted">{merge_.merge_commit_id.slice(0, 12)}</dd>
          </>
        )}
      </dl>

      {latestJob && (
        <ValidationPanel
          job={latestJob}
          busy={validating}
          error={valError}
          onRun={() => void runValidation(latestJob.id)}
          onRetry={() => void retryValidation(latestJob.id)}
        />
      )}

      {done ? (
        <p className="muted note">The work order is complete.</p>
      ) : gateOpen ? (
        <div className="merge-gate">
          <p className="muted">
            {validationBlocks
              ? "Validation checks must pass before this revision can merge."
              : `Both agents approved this revision. Review the PR if you like, then merge #${pr?.number} into the default branch to complete the work order.`}
          </p>
          <div className="row">
            {!confirm ? (
              <button
                className="primary"
                onClick={() => setConfirm(true)}
                disabled={merging || validating || validationBlocks}
                title={validationBlocks ? "Validation must pass first" : undefined}
              >
                Merge
              </button>
            ) : (
              <>
                <button className="primary" onClick={() => void merge()} disabled={merging}>
                  {merging ? "Merging…" : "Confirm merge"}
                </button>
                <button className="ghost" onClick={() => setConfirm(false)} disabled={merging}>
                  Cancel
                </button>
              </>
            )}
            {openPr}
          </div>
        </div>
      ) : (
        <p className="muted">
          The agents are still working toward a mergeable revision. The merge gate opens once review
          passes.
        </p>
      )}
    </section>
  );
}

const VAL_STATUS: Record<ValidationJob["status"], { label: string; tone: string }> = {
  pending: { label: "Not run yet", tone: "muted" },
  running: { label: "Running…", tone: "warn" },
  passed: { label: "Passed ✓", tone: "ok" },
  failed: { label: "Failed", tone: "bad" },
};

function ValidationPanel({
  job,
  busy,
  error,
  onRun,
  onRetry,
}: {
  job: ValidationJob;
  busy: boolean;
  error: string | null;
  onRun: () => void;
  onRetry: () => void;
}) {
  const st = VAL_STATUS[job.status];
  const resultByCommand = new Map(job.results.map((r) => [r.command, r]));
  return (
    <div className="validation-panel">
      <div className="validation-panel__head">
        <h3>Validation</h3>
        <span className={`state state--${st.tone}`}>
          <span className={`dot dot--${st.tone}${job.status === "running" || busy ? " dot--pulse" : ""}`} />
          {busy ? "Running…" : st.label}
        </span>
      </div>
      {error && <div className="banner banner--error">{error}</div>}
      {job.error && job.status === "failed" && <p className="muted note">{job.error}</p>}
      <ul className="validation-panel__cmds">
        {job.commands.map((cmd, i) => {
          const r = resultByCommand.get(cmd);
          const tone = r ? (r.exit_code === 0 ? "ok" : "bad") : "muted";
          return (
            <li key={i} className="validation-cmd">
              <div className="validation-cmd__line">
                <span className={`dot dot--${tone}`} />
                <span className="mono validation-cmd__text">{cmd}</span>
                {r && <span className={`chip chip--${r.exit_code === 0 ? "ok" : "bad"}`}>exit {r.exit_code}</span>}
                {r && <span className="muted validation-cmd__dur">{Math.round(r.duration_ms)}ms</span>}
              </div>
              {r && r.exit_code !== 0 && r.output && (
                <pre className="validation-cmd__output">{r.output}</pre>
              )}
            </li>
          );
        })}
      </ul>
      <div className="row">
        {job.status === "pending" && (
          <button className="primary" onClick={onRun} disabled={busy}>
            {busy ? "Running…" : "Run validation"}
          </button>
        )}
        {job.status === "failed" && (
          <button className="primary" onClick={onRetry} disabled={busy}>
            {busy ? "Running…" : "Retry validation"}
          </button>
        )}
      </div>
    </div>
  );
}
