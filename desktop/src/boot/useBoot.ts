import { useCallback, useEffect, useRef, useState } from "react";
import { coordinatorReachable } from "../api/health";
import { dockerProbe, stackUp, type DockerProbe } from "../ipc";

export type BootPhase = "probing" | "starting" | "finishing" | "docker-down" | "ready";

// Minimum time the loading screen stays up on a healthy start, so a fast boot
// doesn't flash the detailed splash. Measured from boot start, so a slow start
// (pulling images / waiting for the coordinator) simply overruns it.
const MIN_VISIBLE_MS = 6000;
// After everything is up, hold the full bar this long so the snap-to-100% is
// actually seen before we hand off to the workspace.
const FINISH_HOLD_MS = 1000;
// How often we re-check the coordinator while the stack is coming up.
const POLL_MS = 1500;
// The bar eases toward this over MIN_VISIBLE_MS, then snaps to 100 once ready —
// so a slow coordinator reads as "almost there" instead of a frozen bar.
const PROGRESS_CREEP_CAP = 90;
const PROGRESS_TICK_MS = 80;

// Flavour lines cycled while the stack comes up. Themed to the forge splash.
const FLAVOR_MESSAGES = [
  "Waking the agents…",
  "Firing up the forge…",
  "Sharpening the tools…",
  "Unrolling the blueprints…",
  "Stoking the embers…",
  "Oiling the gears…",
  "Briefing the crew…",
];
const FLAVOR_ROTATE_MS = 1900;
const FINISH_MESSAGE = "Forge lit — entering the workshop…";

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

// Smoothstep: starts near empty, eases through the middle, decelerates into the
// cap. Reads as organic rather than a constant-speed linear crawl.
const smoothstep = (t: number) => {
  const x = Math.max(0, Math.min(1, t));
  return x * x * (3 - 2 * x);
};

/**
 * Startup gate: probe Docker, bring the stack up if needed, and hold a loading
 * screen until the coordinator answers. Only a down/missing Docker daemon drops
 * the loading screen early (into a "docker-down" status view the user can act
 * on and re-check). Everything else eases to a full bar, then "ready".
 */
export function useBoot() {
  const [phase, setPhase] = useState<BootPhase>("probing");
  const [detail, setDetail] = useState(FLAVOR_MESSAGES[0]);
  const [probe, setProbe] = useState<DockerProbe | null>(null);
  const [progress, setProgress] = useState(0);
  const [attempt, setAttempt] = useState(0);
  const errorRef = useRef<string | null>(null);

  // Re-run the whole sequence (used by the docker-down "Re-check" button).
  const retry = useCallback(() => setAttempt((a) => a + 1), []);

  useEffect(() => {
    let cancelled = false;
    const startedAt = Date.now();
    setProgress(0);
    errorRef.current = null;

    // Ease the bar toward the cap over the minimum visible window. Monotonic:
    // never walks backwards; the finish path below snaps it to 100.
    const creep = setInterval(() => {
      if (cancelled) return;
      const t = (Date.now() - startedAt) / MIN_VISIBLE_MS;
      const target = smoothstep(t) * PROGRESS_CREEP_CAP;
      setProgress((prev) => (target > prev ? target : prev));
    }, PROGRESS_TICK_MS);

    const holdMinVisible = async () => {
      const remaining = MIN_VISIBLE_MS - (Date.now() - startedAt);
      if (remaining > 0) await wait(remaining);
    };

    const run = async () => {
      setPhase("probing");

      let p: DockerProbe;
      try {
        p = await dockerProbe();
      } catch (e) {
        if (cancelled) return;
        clearInterval(creep);
        setProbe(null);
        setDetail(String(e));
        setPhase("docker-down");
        return;
      }
      if (cancelled) return;
      setProbe(p);

      if (!p.docker_running || !p.compose_available) {
        clearInterval(creep);
        setDetail(""); // not an error, just not up — the view explains it
        setPhase("docker-down");
        return;
      }

      // Docker is up. Bring the stack up unless the coordinator already answers.
      // The visible status is the rotating flavour text; real errors are kept
      // in errorRef and only surfaced if we end up in the docker-down view.
      setPhase("starting");
      if (!(await coordinatorReachable())) {
        if (cancelled) return;
        try {
          await stackUp();
        } catch (e) {
          if (cancelled) return;
          // Keep polling — the coordinator may still finish its own startup
          // even if the compose call errored.
          errorRef.current = String(e);
        }
      }

      while (!cancelled) {
        if (await coordinatorReachable()) break;
        await wait(POLL_MS);
      }
      if (cancelled) return;

      await holdMinVisible();
      if (cancelled) return;

      // Snap the bar full and hold it briefly so the completion is seen before
      // the workspace takes over.
      clearInterval(creep);
      setProgress(100);
      setDetail(FINISH_MESSAGE);
      setPhase("finishing");
      await wait(FINISH_HOLD_MS);
      if (cancelled) return;
      setPhase("ready");
    };

    void run();
    return () => {
      cancelled = true;
      clearInterval(creep);
    };
  }, [attempt]);

  // Rotate flavour text while the stack comes up. Runs across probing+starting
  // (both are "loading") without resetting, and stops for finishing/ready so the
  // finish message sticks and for docker-down so the real error shows.
  const loading = phase === "probing" || phase === "starting";
  useEffect(() => {
    if (!loading) return;
    let i = 0;
    setDetail(FLAVOR_MESSAGES[0]);
    const id = setInterval(() => {
      i = (i + 1) % FLAVOR_MESSAGES.length;
      setDetail(FLAVOR_MESSAGES[i]);
    }, FLAVOR_ROTATE_MS);
    return () => clearInterval(id);
  }, [loading]);

  return { phase, detail, probe, progress, retry };
}
