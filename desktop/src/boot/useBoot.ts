import { useCallback, useEffect, useState } from "react";
import { coordinatorReachable } from "../api/health";
import {
  dockerProbe,
  launchDockerDesktop,
  stackStatus,
  stackUp,
  type DockerProbe,
} from "../ipc";

export type BootPhase =
  | "probing"
  | "launching-docker"
  | "starting"
  | "checking"
  | "finishing"
  | "docker-down"
  | "failed"
  | "ready";

// Minimum time the loading screen stays up on a healthy start, so a fast boot
// doesn't flash the detailed splash. Measured from boot start, so a slow start
// (pulling images / waiting for the coordinator) simply overruns it.
const MIN_VISIBLE_MS = 6000;
// After everything is up, hold the full bar this long so the snap-to-100% is
// actually seen before we hand off to the workspace.
const FINISH_HOLD_MS = 1000;
// How often we re-check the coordinator while the stack is coming up.
const POLL_MS = 1500;
const DOCKER_START_TIMEOUT_MS = 120_000;
// Once Compose has returned, bound readiness polling so a broken container or
// occupied port becomes an actionable retry screen instead of an endless bar.
const STACK_READY_TIMEOUT_MS = 120_000;
const PROGRESS_TICK_MS = 80;
const FINISH_MESSAGE = "Forge lit — entering the workshop…";
const REQUIRED_CORE_SERVICES = ["forgejo", "coordinator", "simulated-codex-worker"];

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));

const stackReady = async () => {
  if (!(await coordinatorReachable())) return false;
  try {
    const statuses = await stackStatus();
    return REQUIRED_CORE_SERVICES.every((service) => {
      const status = statuses.find((candidate) => candidate.service === service);
      return status?.state === "running" && (!status.health || status.health === "healthy");
    });
  } catch {
    return false;
  }
};

/**
 * Startup gate: probe Docker, bring the stack up if needed, and hold a loading
 * screen until every core service is ready. Missing Docker and failed stack
 * startup each become an actionable status view rather than an endless wait.
 */
export function useBoot() {
  const [phase, setPhase] = useState<BootPhase>("probing");
  const [detail, setDetail] = useState("Checking Docker…");
  const [probe, setProbe] = useState<DockerProbe | null>(null);
  const [progress, setProgress] = useState(0);
  const [request, setRequest] = useState({ id: 0, launchDocker: false });

  // Re-run the whole sequence (used by the docker-down "Re-check" button).
  const retry = useCallback(() => {
    setRequest((current) => ({ id: current.id + 1, launchDocker: false }));
  }, []);

  const launchDocker = useCallback(() => {
    setPhase("launching-docker");
    setDetail("Opening Docker Desktop…");
    setRequest((current) => ({ id: current.id + 1, launchDocker: true }));
  }, []);

  useEffect(() => {
    let cancelled = false;
    const startedAt = Date.now();
    let progressCap = 8;
    setProgress(0);

    // Each real startup stage advances the floor and raises a bounded cap. The
    // bar eases toward that cap while the underlying operation is in flight,
    // then the next observed milestone moves it forward without ever resetting.
    const advanceProgress = (floor: number, cap: number) => {
      progressCap = cap;
      setProgress((current) => Math.max(current, floor));
    };
    const creep = setInterval(() => {
      if (cancelled) return;
      setProgress((current) => {
        if (current >= progressCap) return current;
        return Math.min(progressCap, current + Math.max(0.08, (progressCap - current) * 0.025));
      });
    }, PROGRESS_TICK_MS);

    const holdMinVisible = async () => {
      const remaining = MIN_VISIBLE_MS - (Date.now() - startedAt);
      if (remaining > 0) await wait(remaining);
    };

    const run = async () => {
      if (request.launchDocker) {
        setPhase("launching-docker");
        setDetail("Opening Docker Desktop…");
        advanceProgress(2, 28);
      } else {
        setPhase("probing");
        setDetail("Checking Docker…");
        advanceProgress(1, 8);
      }

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

      if (request.launchDocker && (!p.docker_running || !p.compose_available)) {
        setPhase("launching-docker");
        setDetail("Opening Docker Desktop…");
        advanceProgress(8, 28);
        try {
          await launchDockerDesktop();
        } catch (e) {
          if (cancelled) return;
          clearInterval(creep);
          setDetail(String(e));
          setPhase("docker-down");
          return;
        }

        setDetail("Waiting for Docker Desktop to finish starting…");
        const dockerDeadline = Date.now() + DOCKER_START_TIMEOUT_MS;
        while (!cancelled && Date.now() < dockerDeadline) {
          await wait(POLL_MS);
          p = await dockerProbe();
          setProbe(p);
          if (p.docker_running && p.compose_available) break;
        }
        if (cancelled) return;
        if (!p.docker_running || !p.compose_available) {
          clearInterval(creep);
          setDetail("Docker Desktop did not become ready in time. Check Docker, then re-check.");
          setPhase("docker-down");
          return;
        }
      }

      if (!p.docker_running || !p.compose_available) {
        clearInterval(creep);
        setDetail(""); // not an error, just not up — the view explains it
        setPhase("docker-down");
        return;
      }

      // Docker is up. Reconcile every core service and provider worker on each
      // desktop launch; this is idempotent and also starts a provider that was
      // connected after the previous stack run.
      setPhase("starting");
      setDetail("Pulling images and starting local services…");
      advanceProgress(30, 82);
      let startupError: string | null = null;
      try {
        await stackUp();
      } catch (e) {
        if (cancelled) return;
        // Compose may report an error after creating some containers, so give
        // the reconciled stack a short chance to become healthy before
        // surfacing the exact failure.
        startupError = String(e);
      }

      setPhase("checking");
      setDetail("Checking local service health…");
      advanceProgress(82, 96);
      const readyDeadline = Date.now() + (startupError ? 15_000 : STACK_READY_TIMEOUT_MS);
      while (!cancelled && !(await stackReady()) && Date.now() < readyDeadline) {
        await wait(POLL_MS);
      }
      if (cancelled) return;
      if (!(await stackReady())) {
        clearInterval(creep);
        setDetail(
          startupError ??
            "The local services did not become healthy in time. Check Docker and try again.",
        );
        setPhase("failed");
        return;
      }

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
  }, [request]);

  return { phase, detail, probe, progress, retry, launchDocker };
}
