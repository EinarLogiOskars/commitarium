import { useCallback, useEffect, useRef, useState } from "react";
import {
  checkDesktopUpdate,
  installDesktopUpdate,
  onDesktopUpdateProgress,
  type DesktopUpdateInfo,
  type DesktopUpdateProgress,
  type UpdateBlockingWorkOrder,
} from "../ipc";

// The renderer's view of the signed-update flow. The native updater owns
// discovery, verification, download, install, and restart; this hook only
// tracks what to display and invokes the two fixed commands.
//
// `blocked` means a provider turn is live (the coordinator's `running` list is
// non-empty). The backend re-checks that safety itself before download and
// before install, so "update once safe" is purely a renderer convenience: once
// the attention snapshot shows nothing running, we invoke install again. The
// choice is session-only — "Later" just drops it.
export type UpdateStatus =
  | "idle" // no check performed yet
  | "checking"
  | "up_to_date"
  | "available"
  | "blocked"
  | "installing"
  | "error";

export interface DesktopUpdate {
  status: UpdateStatus;
  currentVersion: string | null;
  update: DesktopUpdateInfo | null;
  workOrders: UpdateBlockingWorkOrder[];
  progress: DesktopUpdateProgress | null;
  armedOnceSafe: boolean;
  error: string | null;
  /** Re-run the trusted update check. */
  check: () => void;
  /** Download + install the seen version, then restart. */
  startInstall: () => void;
  /** Retry install automatically once nothing is running. */
  updateOnceSafe: () => void;
  /** Drop the once-safe arming (the "Later" choice). */
  dismiss: () => void;
}

export function useDesktopUpdate(enabled: boolean, runningCount: number): DesktopUpdate {
  const [status, setStatus] = useState<UpdateStatus>("idle");
  const [currentVersion, setCurrentVersion] = useState<string | null>(null);
  const [update, setUpdate] = useState<DesktopUpdateInfo | null>(null);
  const [workOrders, setWorkOrders] = useState<UpdateBlockingWorkOrder[]>([]);
  const [progress, setProgress] = useState<DesktopUpdateProgress | null>(null);
  const [armedOnceSafe, setArmed] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Guards a single in-flight install invocation (also the once-safe retry).
  const attempting = useRef(false);

  // Progress events are the authoritative installation display.
  useEffect(() => {
    const un = onDesktopUpdateProgress((p) => {
      setProgress(p);
      setStatus("installing");
    });
    return () => void un.then((f) => f());
  }, []);

  const check = useCallback(async () => {
    setStatus((s) => (s === "installing" ? s : "checking"));
    setError(null);
    try {
      const res = await checkDesktopUpdate();
      setCurrentVersion(res.current_version);
      if (res.status === "available") {
        setUpdate(res.update);
        setStatus("available");
      } else {
        setUpdate(null);
        setStatus("up_to_date");
      }
    } catch (e) {
      setError(String(e));
      setStatus("error");
    }
  }, []);

  // One automatic check when the stack becomes usable.
  useEffect(() => {
    if (enabled) void check();
  }, [enabled, check]);

  const startInstall = useCallback(async () => {
    if (attempting.current) return;
    const target = update?.version;
    if (!target) return;
    attempting.current = true;
    setError(null);
    try {
      const res = await installDesktopUpdate(target);
      if (res.status === "blocked") {
        setWorkOrders(res.work_orders);
        setStatus("blocked");
      } else {
        setWorkOrders([]);
        setStatus("installing");
      }
    } catch (e) {
      // The seen release may have changed under us (the backend rejects a stale
      // expectedVersion), or discovery failed — surface it and re-check.
      setError(String(e));
      setStatus("error");
      void check();
    } finally {
      attempting.current = false;
    }
  }, [update, check]);

  const updateOnceSafe = useCallback(() => {
    setArmed(true);
    // If it's already safe, go immediately.
    if (runningCount === 0) void startInstall();
  }, [runningCount, startInstall]);

  const dismiss = useCallback(() => {
    setArmed(false);
    if (status === "blocked") setStatus("available");
  }, [status]);

  // Armed + nothing running → retry the install the backend previously blocked.
  useEffect(() => {
    if (armedOnceSafe && status === "blocked" && runningCount === 0 && !attempting.current) {
      void startInstall();
    }
  }, [armedOnceSafe, status, runningCount, startInstall]);

  return {
    status,
    currentVersion,
    update,
    workOrders,
    progress,
    armedOnceSafe,
    error,
    check: () => void check(),
    startInstall: () => void startInstall(),
    updateOnceSafe,
    dismiss,
  };
}
