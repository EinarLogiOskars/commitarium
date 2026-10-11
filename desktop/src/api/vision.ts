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
