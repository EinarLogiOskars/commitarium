import { request } from "./client";
import type { PlanningMessage, Run } from "./types";

/** Start the configured workflow for a draft feature. Idempotent by key. */
export const startRun = (
  projectId: string,
  featureId: string,
  idempotencyKey: string,
): Promise<Run> =>
  request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}/runs`,
    { method: "POST", idempotencyKey },
  );

export const getRun = (runId: string): Promise<Run> =>
  request(`/api/v1/runs/${encodeURIComponent(runId)}`);

const runPath = (runId: string) => `/api/v1/runs/${encodeURIComponent(runId)}`;

export const getPlanningMessages = (runId: string): Promise<PlanningMessage[]> =>
  request(`${runPath(runId)}/planning/messages`);

// Explicit phase actions. Each is empty-body + Idempotency-Key and safe to
// retry; the backend's state gating prevents double-launch.
export const startPlanning = (runId: string, key: string): Promise<Run> =>
  request(`${runPath(runId)}/planning`, { method: "POST", idempotencyKey: key });

export const startPlanningReviewer = (runId: string, key: string): Promise<Run> =>
  request(`${runPath(runId)}/planning/reviewer`, { method: "POST", idempotencyKey: key });

export const startPlanningRound = (runId: string, key: string): Promise<Run> =>
  request(`${runPath(runId)}/planning/round`, { method: "POST", idempotencyKey: key });

export const startImplementation = (runId: string, key: string): Promise<Run> =>
  request(`${runPath(runId)}/implementation`, { method: "POST", idempotencyKey: key });
