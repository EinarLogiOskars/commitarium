import { useEffect, useState } from "react";
import { recoverRun } from "../api/runs";
import { ApiError } from "../api/client";
import type { Run } from "../api/types";

/** "in 2 min", "in 30 s", or "now", until an automatic retry is due. */
export function retryLabel(dueAt: string, now: number = Date.now()): string {
  const due = new Date(dueAt).getTime();
  if (Number.isNaN(due) || due <= now) return "now";
  const seconds = Math.ceil((due - now) / 1000);
  if (seconds < 60) return `in ${seconds} s`;
  return `in ${Math.ceil(seconds / 60)} min`;
}

// A turn infrastructure cut off (a worker restart, a lost connection): nothing
// is wrong with the work, and the coordinator retries it by itself. Retry now
// continues at once; should it be interrupted again, retries start afresh.
export function RetryingBanner({ run, onChanged }: { run: Run; onChanged: () => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 5000);
    return () => window.clearInterval(timer);
  }, []);
  if (!run.retrying) return null;
  const { cause, retry, of, due_at } = run.retrying;
  const retryNow = async () => {
    setBusy(true);
    setError(null);
    try {
      await recoverRun(run.id, crypto.randomUUID(), { action: "continue" });
      onChanged();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="wait-banner usage-limit">
      <span className="wait-banner__kind">Interrupted — retrying {retryLabel(due_at, now)}</span>
      <span>{`The turn was interrupted (${cause}). Commitarium retries it by itself (retry ${retry} of ${of}).`}</span>
      <button className="ghost" onClick={() => void retryNow()} disabled={busy}>
        {busy ? "Retrying…" : "Retry now"}
      </button>
      {error && <span className="usage-limit__error">{error}</span>}
    </div>
  );
}
