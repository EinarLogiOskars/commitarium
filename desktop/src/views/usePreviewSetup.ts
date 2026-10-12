import { useCallback, useEffect, useRef, useState } from "react";
import { listFeatures, reportPreviewFailure, startPreviewSetup } from "../api/features";
import { ApiError } from "../api/client";
import type { Feature } from "../api/types";
import type { PreviewHandle } from "./usePreview";

const POLL_MS = 5000;

/** The project's background preview setup: the work order setting the preview
 * up, if one runs. When one merges, the preview starts by itself; when that
 * start fails, a background fix is asked for, until the fixes are used up and
 * the user is offered a session instead. */
export function usePreviewSetup(projectId: string, preview: PreviewHandle) {
  const [active, setActive] = useState<Feature | null>(null);
  const [exhausted, setExhausted] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const previous = useRef<Feature | null>(null);
  // Stable per project: the preview handle itself is a new object each render.
  const startPreview = preview.start;
  // Set while a preview the setup started is still proving itself.
  const autoStarted = useRef(false);

  const load = useCallback(async () => {
    try {
      const features = await listFeatures(projectId);
      const running = features.find((f) => f.background && !isDone(f)) ?? null;
      const before = previous.current;
      previous.current = running;
      setActive(running);
      if (before && !running) {
        const finished = features.find((f) => f.id === before.id);
        if (finished?.state === "completed") {
          autoStarted.current = true;
          setExhausted(false);
          void startPreview();
        }
      }
    } catch {
      /* keep the last state */
    }
  }, [projectId, startPreview]);

  useEffect(() => {
    previous.current = null;
    autoStarted.current = false;
    setActive(null);
    setExhausted(false);
    void load();
    const id = setInterval(() => void load(), POLL_MS);
    return () => clearInterval(id);
  }, [load]);

  // A preview the setup started that fails asks for a background fix.
  const state = preview.status?.state;
  const failure = preview.status?.error ?? preview.error;
  useEffect(() => {
    if (!autoStarted.current) return;
    if (state === "running") {
      autoStarted.current = false;
      return;
    }
    if (state !== "failed" || !failure) return;
    autoStarted.current = false;
    reportPreviewFailure(projectId, failure).then(
      (setup) => {
        if (setup.work_order) {
          previous.current = setup.work_order;
          setActive(setup.work_order);
        }
      },
      (e) => {
        if (e instanceof ApiError && e.code === "preview_fixes_exhausted") setExhausted(true);
      },
    );
  }, [projectId, state, failure]);

  const start = useCallback(async () => {
    setError(null);
    try {
      const setup = await startPreviewSetup(projectId);
      if (setup.work_order) {
        previous.current = setup.work_order;
        setActive(setup.work_order);
      }
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  }, [projectId]);

  return { active, exhausted, error, start };
}

export type PreviewSetupHandle = ReturnType<typeof usePreviewSetup>;

function isDone(f: Feature): boolean {
  return f.state === "completed" || f.state === "cancelled";
}
