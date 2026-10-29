import { useState } from "react";
import type { Vision } from "../api/types";
import { discardPlanning, pausePlanning, startPlanning, type PlanningStart } from "../api/vision";
import { ASSISTANT_CHANGED, WORK_CHANGED } from "./chatEvents";
import type { ChatPlanning } from "./planMode";

// Plan mode's switch (ADR-039), shared by the cards' Plan mode button, the
// composer's + menu and /plan, and the chat header.

/** Turns plan mode on in chat (planning starts, resumes, or moves there,
 * with goal seeding a project without a vision, and start saying whether
 * the user came with something to plan) or off, with the plan kept as it
 * is. The project's cards and chats reload. */
export async function switchPlanMode(
  projectId: string,
  goal: string,
  chat: string | undefined,
  on: boolean,
  start: PlanningStart = {},
): Promise<Vision> {
  const vision = on
    ? await startPlanning(projectId, goal, chat, start)
    : await pausePlanning(projectId);
  window.dispatchEvent(new Event(WORK_CHANGED));
  window.dispatchEvent(new Event(ASSISTANT_CHANGED));
  return vision;
}

/** Switches plan mode off without keeping the plan: the session's draft,
 * notes, and unbuilt plan changes go, and the vision is as it was before it
 * (null when the session had made it). */
export async function discardPlanMode(projectId: string): Promise<Vision | null> {
  const { vision } = await discardPlanning(projectId);
  window.dispatchEvent(new Event(WORK_CHANGED));
  window.dispatchEvent(new Event(ASSISTANT_CHANGED));
  return vision;
}

/** The plan switch of a chat: toggle turns it on, or, while it is on, asks
 * how to stop (stopping: keep the plan for next time, discard it, or
 * cancel; StopPlanDialog asks it), and start turns it on unless it is, with
 * what the user came to plan. Each answers whether it worked; an error
 * shows through onError, or in error without one. */
export function usePlanSwitch(
  projectId: string,
  goal: string,
  chat: string | undefined,
  planning: ChatPlanning,
  options: {
    onChanged?: (vision: Vision | null) => void;
    onError?: (message: string) => void;
  } = {},
) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Turning it off asks first how to stop.
  const [stopping, setStopping] = useState(false);
  const on = planning.kind === "here";
  const run = async (change: () => Promise<Vision | null>): Promise<boolean> => {
    setBusy(true);
    setError(null);
    try {
      options.onChanged?.(await change());
      return true;
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e);
      if (options.onError) options.onError(message);
      else setError(message);
      return false;
    } finally {
      setBusy(false);
    }
  };
  const turnOn = (start: PlanningStart = {}) =>
    run(() => switchPlanMode(projectId, goal, chat, true, start));
  const stop = async (change: () => Promise<Vision | null>) => {
    const done = await run(change);
    if (done) setStopping(false);
    return done;
  };
  return {
    on,
    busy,
    error,
    stopping,
    toggle: (): Promise<boolean> => {
      if (!on) return turnOn();
      setError(null);
      setStopping(true);
      return Promise.resolve(true);
    },
    start: (start: PlanningStart = {}) => (on ? Promise.resolve(true) : turnOn(start)),
    /** Stop, with the plan kept as it is for next time (paused). */
    keep: () => stop(() => switchPlanMode(projectId, goal, chat, false)),
    /** Stop, dropping this session's unbuilt plan. */
    discard: () => stop(() => discardPlanMode(projectId)),
    cancel: () => setStopping(false),
  };
}
