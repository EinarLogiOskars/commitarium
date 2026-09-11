import { request } from "./client";
import type { SessionEvent } from "./types";

export const getSessionEvents = (sessionId: string): Promise<SessionEvent[]> =>
  request(`/api/v1/sessions/${encodeURIComponent(sessionId)}/events`);

/** Send a message to a waiting session (e.g. a clarification reply). */
export const sendSessionMessage = (
  sessionId: string,
  message: string,
  idempotencyKey: string,
): Promise<unknown> =>
  request(`/api/v1/sessions/${encodeURIComponent(sessionId)}/commands`, {
    method: "POST",
    idempotencyKey,
    body: { type: "message", message },
  });

/** Accept the clarified goal from a waiting draft lead session. */
export const acceptGoal = (
  sessionId: string,
  goal: string,
  idempotencyKey: string,
): Promise<unknown> =>
  request(`/api/v1/sessions/${encodeURIComponent(sessionId)}/goal-acceptance`, {
    method: "POST",
    idempotencyKey,
    body: { goal },
  });
