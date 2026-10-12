import { useState } from "react";
import { recoverRun } from "../api/runs";
import { ApiError } from "../api/client";
import type { Run } from "../api/types";

/** "14:00", or "tomorrow 14:00", for a limit's reset time. */
export function resetLabel(resetsAt?: string): string | null {
  if (!resetsAt) return null;
  const at = new Date(resetsAt);
  if (Number.isNaN(at.getTime())) return null;
  const time = at.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  const today = new Date();
  if (at.toDateString() === today.toDateString()) return time;
  return `${at.toLocaleDateString([], { weekday: "short" })} ${time}`;
}

// A run paused on an agent's usage limit: nothing is wrong, and the work
// continues by itself when the limit resets. Try now continues at once, for a
// limit that reset early; a limit still reached just pauses it again.
export function UsageLimitBanner({
  run,
  agentLabel,
  onChanged,
}: {
  run: Run;
  agentLabel: string;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  if (!run.usage_limit) return null;
  const at = resetLabel(run.usage_limit.resets_at);
  const tryNow = async () => {
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
    <div className="wait-banner wait-banner--warn usage-limit">
      <span className="wait-banner__kind">{agentLabel} usage limit reached</span>
      <span>
        {at
          ? `The work continues by itself when it resets at ${at}.`
          : "The work continues by itself once it resets; Commitarium tries again every 15 minutes."}
      </span>
      <button className="ghost" onClick={() => void tryNow()} disabled={busy}>
        {busy ? "Trying…" : "Try now"}
      </button>
      {error && <span className="usage-limit__error">{error}</span>}
    </div>
  );
}
