import { useCallback, useEffect, useState } from "react";
import { coordinatorFreshness, rebuildCoordinator } from "../ipc";

const CHECK_INTERVAL_MS = 5 * 60_000;
const BUSY_RETRY_MS = 60_000;

type Status = "hidden" | "stale" | "rebuilding" | "waiting";

// Running from source, says when the coordinator was built before the newest
// commit to its sources (a pull without rebuilding it), and rebuilds it.
export function CoordinatorBanner() {
  const [status, setStatus] = useState<Status>("hidden");
  const [error, setError] = useState<string | null>(null);

  const check = useCallback(async () => {
    try {
      const freshness = await coordinatorFreshness();
      setStatus((current) =>
        current === "rebuilding" || current === "waiting"
          ? current
          : freshness === "stale"
            ? "stale"
            : "hidden",
      );
    } catch {
      // Unknown: say nothing.
    }
  }, []);

  useEffect(() => {
    void check();
    const id = setInterval(() => void check(), CHECK_INTERVAL_MS);
    return () => clearInterval(id);
  }, [check]);

  const rebuild = useCallback(async () => {
    setStatus("rebuilding");
    setError(null);
    try {
      if ((await rebuildCoordinator()) === "busy") {
        setStatus("waiting");
        return;
      }
      setStatus("hidden");
      void check();
    } catch (e) {
      setError(String(e));
      setStatus("stale");
    }
  }, [check]);

  // Running work holds the restart; try again every minute.
  useEffect(() => {
    if (status !== "waiting") return;
    const id = setTimeout(() => void rebuild(), BUSY_RETRY_MS);
    return () => clearTimeout(id);
  }, [status, rebuild]);

  if (status === "hidden") return null;
  return (
    <div className="banner banner--warn coordinator-banner" role="status">
      <span>The coordinator is older than your code.</span>
      {status === "waiting" && (
        <span className="muted">Waiting for running work to finish before rebuilding.</span>
      )}
      {error && <span className="coordinator-banner__error">{error}</span>}
      <button className="ghost" onClick={() => void rebuild()} disabled={status === "rebuilding"}>
        {status === "rebuilding" ? "Rebuilding…" : status === "waiting" ? "Try now" : "Rebuild"}
      </button>
    </div>
  );
}
