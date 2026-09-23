import { useCallback, useEffect, useRef, useState } from "react";
import {
  ENV_ACTIVE,
  approveEnvironmentRequest,
  listEnvironmentRequests,
  rejectEnvironmentRequest,
} from "../api/environments";
import { provisionEnvironmentRequest } from "../ipc";
import { ApiError } from "../api/client";
import type { EnvironmentRequest } from "../api/types";

const POLL_MS = 3000;

// When an implementation lead is blocked on missing Debian packages, the user
// approves or rejects the exact list. Approval triggers a native image rebuild
// (installation-wide — it applies to every agent + validation container). Shown
// in the work-order view; while a request is active the generic blocker
// "Re-check" action is suppressed (this panel is the real action).
export function EnvironmentApproval({
  projectId,
  runId,
  onResolved,
  onActiveChange,
}: {
  projectId: string;
  runId: string;
  onResolved: () => void;
  onActiveChange?: (active: boolean) => void;
}) {
  const [req, setReq] = useState<EnvironmentRequest | null>(null);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState<null | "approve" | "provision" | "reject">(null);
  const [error, setError] = useState<string | null>(null);
  const activeRef = useRef(false);

  const load = useCallback(async () => {
    try {
      const { requests } = await listEnvironmentRequests(projectId);
      const active =
        requests
          .filter((r) => r.run_id === runId && ENV_ACTIVE.has(r.status))
          .sort((a, b) => b.requested_at.localeCompare(a.requested_at))[0] ?? null;
      setReq(active);
      if (activeRef.current !== !!active) {
        activeRef.current = !!active;
        onActiveChange?.(!!active);
      }
    } catch {
      /* transient */
    }
  }, [projectId, runId, onActiveChange]);

  useEffect(() => {
    void load();
    const id = setInterval(() => void load(), POLL_MS);
    return () => clearInterval(id);
  }, [load]);

  // Provisioning rebuilds images and can take minutes; the native call resolves
  // when done (or failed). Then refresh so a ready request resumes the run.
  const provision = async (id: string) => {
    setBusy("provision");
    setError(null);
    try {
      await provisionEnvironmentRequest(id);
      await load();
      onResolved();
    } catch (e) {
      setError(describe(e));
      await load();
    } finally {
      setBusy(null);
    }
  };

  const approve = async (id: string) => {
    setBusy("approve");
    setError(null);
    try {
      await approveEnvironmentRequest(id);
      await provision(id); // approval then rebuild is one user intent
    } catch (e) {
      setError(describe(e));
      setBusy(null);
    }
  };

  const reject = async (id: string) => {
    setBusy("reject");
    setError(null);
    try {
      await rejectEnvironmentRequest(id, reason.trim());
      await load();
      onResolved();
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
    }
  };

  if (!req) return null;

  const provisioning = busy === "provision" || req.status === "provisioning";
  const working = busy !== null || provisioning;

  return (
    <section className="panel env-approval">
      <div className="panel__head">
        <h2>Package approval needed</h2>
        <span className="pill pill--warn">{req.status}</span>
      </div>
      {error && <div className="banner banner--error">{error}</div>}
      <p className="muted">
        The lead can't continue without these Debian packages. Approving installs them into{" "}
        <strong>all</strong> agent and validation containers (installation-wide), not your host.
      </p>
      {req.reason && <p className="env-approval__reason">{req.reason}</p>}
      <div className="stack__chips env-approval__pkgs">
        {req.system_packages.map((p) => (
          <span key={p} className="pill">
            {p}
          </span>
        ))}
      </div>

      {provisioning ? (
        <p className="muted note">
          <span className="dot dot--warn dot--pulse" /> Rebuilding agent images with the approved
          packages… this can take a few minutes.
        </p>
      ) : req.status === "failed" ? (
        <>
          {req.error && <p className="muted note">{req.error}</p>}
          <div className="row">
            <button className="primary" onClick={() => void provision(req.id)} disabled={working}>
              Retry provisioning
            </button>
            <button className="ghost danger" onClick={() => void reject(req.id)} disabled={working}>
              Reject
            </button>
          </div>
        </>
      ) : req.status === "approved" ? (
        <div className="row">
          <button className="primary" onClick={() => void provision(req.id)} disabled={working}>
            Provision now
          </button>
          <button className="ghost danger" onClick={() => void reject(req.id)} disabled={working}>
            Reject
          </button>
        </div>
      ) : (
        <>
          <label className="env-approval__reason-input">
            Rejection reason (optional)
            <input
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              placeholder="Why these packages aren't acceptable…"
              disabled={working}
            />
          </label>
          <div className="row">
            <button className="primary" onClick={() => void approve(req.id)} disabled={working}>
              {busy === "approve" ? "Approving…" : "Approve & install"}
            </button>
            <button className="ghost danger" onClick={() => void reject(req.id)} disabled={working}>
              {busy === "reject" ? "Rejecting…" : "Reject"}
            </button>
          </div>
        </>
      )}
    </section>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}
