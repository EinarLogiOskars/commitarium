import { useCallback, useEffect, useRef, useState } from "react";
import { listAttention } from "../api/attention";
import { ApiError } from "../api/client";
import type { AttentionItem, RunningWorkOrder } from "../api/types";

const POLL_MS = 12000;
// A hidden window still polls, just rarely: notifications exist precisely for
// the time the user isn't looking, so pausing entirely would defeat them.
const HIDDEN_POLL_MS = 45000;

export interface AttentionSnapshot {
  /** Everything the coordinator reports, already sorted actionable-first. */
  items: AttentionItem[];
  /** The subset that is waiting on a human; the badge counts these. */
  actionable: AttentionItem[];
  /** Work orders with a run in flight right now, across every project. */
  running: RunningWorkOrder[];
  /** False once the coordinator answers 404 — a build without the inbox. */
  supported: boolean;
  loaded: boolean;
  refresh: () => void;
}

// One cross-project poller for the whole app. The coordinator owns the
// classification, so the inbox, the per-project dashboard and the notifier all
// read this single snapshot instead of re-deriving attention from runs.
export function useAttention(enabled: boolean): AttentionSnapshot {
  const [items, setItems] = useState<AttentionItem[]>([]);
  const [running, setRunning] = useState<RunningWorkOrder[]>([]);
  const [supported, setSupported] = useState(true);
  const [loaded, setLoaded] = useState(false);
  // Read inside the poll closure so giving up doesn't re-create the interval.
  const supportedRef = useRef(true);

  const poll = useCallback(async () => {
    if (!supportedRef.current) return;
    try {
      const snapshot = await listAttention();
      setItems(snapshot.items);
      setRunning(snapshot.running);
      setLoaded(true);
    } catch (e) {
      // The route is only registered when the coordinator runs the full
      // project/feature/execution set. A 404 is permanent for this process:
      // stop asking and let callers hide the surface entirely. Anything else
      // (stack restarting, transient network) keeps the last good snapshot.
      if (e instanceof ApiError && e.status === 404) {
        supportedRef.current = false;
        setSupported(false);
      }
    }
  }, []);

  // Stable, so callers can depend on it from an effect without re-subscribing.
  const refresh = useCallback(() => void poll(), [poll]);

  useEffect(() => {
    if (!enabled) return;
    let stopped = false;
    let inFlight = false;
    let timer: number | undefined;
    // Self-scheduling rather than a fixed interval: the snapshot is a full
    // cross-project fan-out on the coordinator, so back off while hidden and
    // resume the fast cadence the moment the window comes back.
    const run = async () => {
      if (stopped || inFlight) return;
      inFlight = true;
      try {
        await poll();
      } finally {
        inFlight = false;
      }
      if (stopped) return;
      window.clearTimeout(timer);
      timer = window.setTimeout(() => void run(), document.hidden ? HIDDEN_POLL_MS : POLL_MS);
    };
    const onVisibility = () => {
      if (document.hidden) return;
      window.clearTimeout(timer);
      void run();
    };
    void run();
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      stopped = true;
      window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [enabled, poll]);

  return {
    items,
    actionable: items.filter((i) => i.actionable),
    running,
    supported,
    loaded,
    refresh,
  };
}
