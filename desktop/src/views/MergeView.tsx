import { useCallback, useEffect, useState } from "react";
import { getRun, mergeRun } from "../api/runs";
import { getWorkspace } from "../api/features";
import { openExternal } from "../ipc";
import { ApiError } from "../api/client";
import type { Workspace } from "../api/types";

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
  const [error, setError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [merging, setMerging] = useState(false);

  const poll = useCallback(async () => {
    try {
      await getRun(runId); // keep the run warm / surface auth errors
      setWorkspace(await getWorkspace(projectId, featureId));
    } catch (e) {
      if (e instanceof ApiError) setError(`${e.message} (${e.code})`);
      // a missing workspace is not an error worth showing
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
      setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    } finally {
      setMerging(false);
    }
  };

  const pr = workspace?.pull_request;
  const merge_ = workspace?.merge;
  const merged = merge_?.merged_at;
  const done = merged || state === "completed";

  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Merge</h2>
        {pr && (
          <button onClick={() => void openExternal(pr.url)} title={pr.url}>
            Open PR #{pr.number} ↗
          </button>
        )}
      </div>
      {error && <div className="banner banner--error">{error}</div>}

      <dl className="detail">
        <dt>Pull request</dt>
        <dd>
          {pr ? `#${pr.number}${pr.draft ? " (draft)" : ""}` : "not opened yet"}
        </dd>
        <dt>Status</dt>
        <dd className={done ? "" : "muted"}>
          {done ? "Merged into the default branch ✓" : state === "ready_to_merge" ? "Ready to merge" : "Awaiting review"}
        </dd>
        {merge_?.merge_commit_id && (
          <>
            <dt>Merge commit</dt>
            <dd className="muted">{merge_.merge_commit_id.slice(0, 12)}</dd>
          </>
        )}
      </dl>

      {done ? (
        <p className="muted note">The work order is complete.</p>
      ) : live && state === "ready_to_merge" ? (
        <div className="merge-gate">
          <p className="muted">
            Both agents approved this revision. Merge PR #{pr?.number} into the default
            branch to complete the work order.
          </p>
          {!confirm ? (
            <button className="primary" onClick={() => setConfirm(true)} disabled={merging}>
              Merge
            </button>
          ) : (
            <div className="row">
              <button className="primary" onClick={() => void merge()} disabled={merging}>
                {merging ? "Merging…" : "Confirm merge"}
              </button>
              <button className="ghost" onClick={() => setConfirm(false)} disabled={merging}>
                Cancel
              </button>
            </div>
          )}
        </div>
      ) : (
        <p className="muted">
          The agents are still working toward a mergeable revision. The merge gate opens
          once review passes.
        </p>
      )}
    </section>
  );
}
