import { useCallback, useEffect, useState } from "react";
import {
  getRealAppStatus,
  openAppWindow,
  openRealAppInBrowser,
  realApp as readRealApp,
  startRealApp,
  type RealApp,
  type RealAppOpened,
} from "../ipc";

// The real app, as the preview bar and the Apps page offer it
// (HELPERS_PLAN.md H5): the app the user uses, with its real data, never the
// preview, which is a test copy with test data. It is the project's
// deployment, a stack of its own. On this computer it follows the main
// branch and opens in its own Commitarium window or in the browser at its
// local address (H7); on a server it is deployed explicitly and opens in the
// browser at its domain or Tailscale address (ADR-040).

/** What to offer: the buttons in order, the one used last first, or why
 * there is nothing to open yet. */
export type RealAppOffer =
  { kind: "open"; buttons: RealAppOpened[] } | { kind: "missing"; text: string };

export function realAppOffer(real: RealApp, isApp: boolean): RealAppOffer {
  if (real.on_server) {
    if (!real.deployed) return { kind: "missing", text: "Your real app isn't deployed yet." };
    if (!real.address)
      return { kind: "missing", text: "Your real app is deployed but has no address yet." };
    return { kind: "open", buttons: ["browser"] };
  }
  // On this computer an app is installed once it is on the Apps page: its
  // real app then starts when it is opened.
  if (!isApp) return { kind: "missing", text: "Your real app isn't installed yet." };
  return {
    kind: "open",
    buttons: real.last === "browser" ? ["browser", "window"] : ["window", "browser"],
  };
}

export const REAL_APP_LABEL: Record<RealAppOpened, string> = {
  window: "Open in Commitarium",
  browser: "Open in browser",
};

// How long opening in the browser waits for an app that was not running.
const START_WAIT_MS = 5 * 60_000;
const START_POLL_MS = 1_000;

/** Opens the real app the chosen way. On this computer an app that is not
 * running is started first, and opens in the browser once it runs. */
export async function openRealApp(
  projectId: string,
  name: string,
  how: RealAppOpened,
  real: RealApp | null,
): Promise<void> {
  if (how === "window") return openAppWindow(projectId, name);
  if (!real?.on_server && !real?.address) await runLocally(projectId);
  return openRealAppInBrowser(projectId);
}

async function runLocally(projectId: string): Promise<void> {
  let status = await getRealAppStatus(projectId);
  if (status?.state !== "running" && status?.state !== "starting") {
    status = await startRealApp(projectId);
  }
  const until = Date.now() + START_WAIT_MS;
  while (status?.state !== "running") {
    if (status?.state === "failed") throw new Error("The app could not start.");
    if (status?.state === "stopped" || Date.now() > until)
      throw new Error("The app isn't running yet.");
    await new Promise((done) => setTimeout(done, START_POLL_MS));
    status = await getRealAppStatus(projectId);
  }
}

// How often the offer is read again: an app may be deployed, or start, meanwhile.
const READ_MS = 15_000;

/** A project's real app, read now and then, with a way to open it. */
export function useRealApp(projectId: string, name: string) {
  const [real, setReal] = useState<RealApp | null>(null);
  const [busy, setBusy] = useState<RealAppOpened | null>(null);
  const [error, setError] = useState<string | null>(null);

  const read = useCallback(() => {
    readRealApp(projectId)
      .then(setReal)
      .catch(() => undefined);
  }, [projectId]);

  useEffect(() => {
    read();
    const timer = setInterval(read, READ_MS);
    return () => clearInterval(timer);
  }, [read]);

  const open = async (how: RealAppOpened) => {
    setBusy(how);
    setError(null);
    try {
      await openRealApp(projectId, name, how, real);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(null);
      // The used button comes first from now on.
      read();
    }
  };

  return { real, busy, error, open };
}
