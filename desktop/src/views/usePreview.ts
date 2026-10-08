import { useCallback, useEffect, useRef, useState } from "react";
import {
  getPreviewStatus,
  onPreviewStatusChanged,
  openExternal,
  startPreview,
  stopPreview,
  type PreviewState,
  type PreviewStatus,
} from "../ipc";

export const PREVIEW_STATE: Record<
  PreviewState,
  { label: string; tone: "ok" | "warn" | "bad" | "muted"; pulse?: boolean }
> = {
  starting: { label: "Starting…", tone: "warn", pulse: true },
  running: { label: "Running", tone: "ok" },
  failed: { label: "Failed", tone: "bad" },
  stopped: { label: "Not running", tone: "muted" },
};

export type PreviewHandle = ReturnType<typeof usePreview>;

/** The URL a preview opens in the browser: the `open` process, else the first. */
export const primaryUrl = (s: PreviewStatus | null) =>
  s?.urls.find((u) => u.open)?.url ?? s?.urls[0]?.url ?? null;

/** One project's preview: live status from the backend, plus start/stop. A
 * user-started preview opens in the browser once it's running (the port
 * changes on every start, so restarts open again too). */
export function usePreview(projectId: string) {
  const [status, setStatus] = useState<PreviewStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const openWhenRunning = useRef(false);

  useEffect(() => {
    let live = true;
    setStatus(null);
    setError(null);
    openWhenRunning.current = false;
    getPreviewStatus(projectId).then(
      (s) => live && setStatus(s),
      () => {
        /* no preview backend yet — show as not running */
      },
    );
    const unlisten = onPreviewStatusChanged((s) => {
      if (live && s.projectId === projectId) setStatus(s);
    });
    return () => {
      live = false;
      void unlisten.then((u) => u());
    };
  }, [projectId]);

  useEffect(() => {
    if (status?.state === "running" && openWhenRunning.current) {
      openWhenRunning.current = false;
      const url = primaryUrl(status);
      if (url) void openExternal(url);
    } else if (status?.state === "failed") {
      // Not "stopped": starting again first stops the old preview, and that
      // event must not cancel opening the new one. stop() clears it itself.
      openWhenRunning.current = false;
    }
  }, [status]);

  const start = useCallback(async () => {
    setError(null);
    openWhenRunning.current = true;
    try {
      setStatus(await startPreview(projectId));
    } catch (e) {
      openWhenRunning.current = false;
      setError(String(e));
    }
  }, [projectId]);

  const stop = useCallback(async () => {
    setError(null);
    openWhenRunning.current = false;
    try {
      await stopPreview(projectId);
    } catch (e) {
      setError(String(e));
    }
  }, [projectId]);

  return { status, error, start, stop };
}
