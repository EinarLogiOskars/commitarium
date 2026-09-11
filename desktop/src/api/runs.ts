import { request } from "./client";
import type { Run } from "./types";

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
