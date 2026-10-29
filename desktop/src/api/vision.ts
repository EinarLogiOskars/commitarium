import { request } from "./client";
import type {
  Feature,
  ProjectToolchain,
  Vision,
  VisionMode,
  VisionPlan,
  VisionSession,
} from "./types";

const visionPath = (id: string) => `/api/v1/projects/${encodeURIComponent(id)}/vision`;

/** The project's vision; 404 vision_not_found until one is written. */
export const getVision = (id: string): Promise<Vision> => request(visionPath(id));

/** Replace the vision with the user's edit; 409 vision_conflict if it changed. */
export const saveVision = (
  id: string,
  plan: VisionPlan,
  expectedRevision: number,
): Promise<Vision> =>
  request(visionPath(id), { method: "PUT", body: { plan, expected_revision: expectedRevision } });

/** Ask the assistant to write the vision, at once or after asking questions. */
export const startVisionAssistant = (
  id: string,
  mode: VisionMode,
  message: string,
  idempotencyKey: string,
): Promise<VisionSession> =>
  request(`${visionPath(id)}/assistant`, {
    method: "POST",
    body: { mode, message },
    idempotencyKey,
  });

/** The project's conversation still running or waiting for the user; 404
 * vision_not_found when there is none. */
export const getOpenVisionSession = (id: string): Promise<VisionSession> =>
  request(`${visionPath(id)}/assistant`);

export const getVisionSession = (id: string, sessionId: string): Promise<VisionSession> =>
  request(`${visionPath(id)}/assistant/${encodeURIComponent(sessionId)}`);

export const replyToVisionAssistant = (
  id: string,
  sessionId: string,
  message: string,
  idempotencyKey: string,
): Promise<VisionSession> =>
  request(`${visionPath(id)}/assistant/${encodeURIComponent(sessionId)}/messages`, {
    method: "POST",
    body: { message },
    idempotencyKey,
  });

/** Create (or return) the draft work order delivering an item without sub-items. */
export const startVisionWorkOrder = (
  id: string,
  itemId: string,
): Promise<{ work_order: Feature; vision: Vision }> =>
  request(`${visionPath(id)}/items/${encodeURIComponent(itemId)}/work-order`, { method: "POST" });

/** Set a new project up: save the stack from runtime presets and start the
 * visible work order that scaffolds the app and its preview. A running setup
 * is answered again. 409 project_has_code once the repository has code. */
export const setUpProject = (
  id: string,
  presets: string[],
  frameworks: string,
): Promise<{ work_order: Feature; toolchain: ProjectToolchain }> =>
  request(`/api/v1/projects/${encodeURIComponent(id)}/setup`, {
    method: "POST",
    body: { presets, frameworks },
  });

/** How planning starts: idea is what the user wants to plan, when they came
 * with one; withMessage says their message with it is sent right after.
 * Without either, the assistant opens by asking what to plan. */
export interface PlanningStart {
  idea?: string;
  withMessage?: boolean;
}

/** Plan the project's vision with the assistant, from goal when it has none
 * yet, or resume paused planning; its assistant is woken to plan it with the
 * user. With chat, planning belongs to that chat, and moves there from
 * another (ADR-039). */
export const startPlanning = (
  id: string,
  goal: string,
  chat?: string,
  start: PlanningStart = {},
): Promise<Vision> =>
  request(`${visionPath(id)}/planning`, {
    method: "POST",
    body: {
      goal,
      ...(chat ? { chat } : {}),
      ...(start.idea ? { idea: start.idea } : {}),
      ...(start.withMessage ? { with_message: true } : {}),
    },
  });

/** Stop planning: the plan is kept as it is and nothing is built; starting
 * planning again resumes it. */
export const pausePlanning = (id: string): Promise<Vision> =>
  request(`${visionPath(id)}/planning/pause`, { method: "POST" });

/** Switch plan mode off without keeping the plan: the session's draft,
 * notes, and unbuilt plan changes are dropped and the vision is put back as
 * it was before it, built work kept. vision is null when the session had
 * created it. */
export const discardPlanning = (id: string): Promise<{ vision: Vision | null; removed: boolean }> =>
  request(`${visionPath(id)}/planning/discard`, { method: "POST" });

/** Build it: the vision is the plan as it stands. With autopilot, the
 * assistant sets the project up and builds it; otherwise the user does. */
export const finishPlanning = (id: string, autopilot: boolean): Promise<Vision> =>
  request(`${visionPath(id)}/planning/finish`, { method: "POST", body: { autopilot } });

/** Add a top-level item to a priority of the vision, optionally linking a
 * work order done outside the plan. */
export const addVisionItem = (
  id: string,
  item: { priority: number; title: string; description: string; work_order_id?: string },
): Promise<{ vision: Vision; item_id: string }> =>
  request(`${visionPath(id)}/items`, { method: "POST", body: item });
