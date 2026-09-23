import { request } from "./client";
import type { InterventionTargetRole, PlanningMessage, Run, ValidationJob } from "./types";

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

/** Merge the approved revision (the ready_to_merge gate). */
export const mergeRun = (runId: string, key: string): Promise<Run> =>
  request(`${runPath(runId)}/merge`, { method: "POST", idempotencyKey: key });

// Run-level pause/resume: a coordinator handoff gate, not an OS freeze. Pause
// stops automatic handoffs at the next safe boundary; resume clears it (and,
// under run_to_completion, dispatches any retained checkpoint). Reusing a key
// for the opposite action returns 409 idempotency_conflict, so keys differ.
export const pauseRun = (runId: string, key: string): Promise<Run> =>
  request(`${runPath(runId)}/pause`, { method: "POST", idempotencyKey: key });

export const resumeRun = (runId: string, key: string): Promise<Run> =>
  request(`${runPath(runId)}/resume`, { method: "POST", idempotencyKey: key });

// Re-check and reconcile the exact durable attempt behind a recovery blocker
// (wait_kind === "blocker"). Never starts a replacement agent; advances the
// workflow if the attempt is now confirmable, otherwise stays blocked.
export const recoverRun = (runId: string, key: string): Promise<Run> =>
  request(`${runPath(runId)}/recover`, { method: "POST", idempotencyKey: key });

// Queue a user message for the lead or reviewer and arm the pause gate. Delivery
// happens at the next safe boundary (a following backend slice); until then the
// queue state is observable but Send stays disabled. Only one unfinished
// intervention may exist per run; resume is rejected until it is answered.
export const queueIntervention = (
  runId: string,
  target: InterventionTargetRole,
  message: string,
  key: string,
): Promise<Run> =>
  request(`${runPath(runId)}/interventions`, {
    method: "POST",
    idempotencyKey: key,
    body: { target, message },
  });

// --- Isolated validation jobs (Phase 4) ---
// Native execution (claim/run/complete) is a Tauri command (runValidationJob);
// these are the coordinator reads + the create/retry triggers.

export const getRunValidationJobs = (runId: string): Promise<{ jobs: ValidationJob[] }> =>
  request(`${runPath(runId)}/validation-jobs`);

export const getValidationJob = (jobId: string): Promise<ValidationJob> =>
  request(`/api/v1/validation-jobs/${encodeURIComponent(jobId)}`);

// Create or idempotently return the job for the exact approved commit + current
// command list (used when the gate was reached before commands were configured).
export const ensureValidationJob = (runId: string): Promise<ValidationJob> =>
  request(`${runPath(runId)}/validation-jobs`, { method: "POST" });

// Create a new pending job from a terminal one; returns the new job to run.
export const retryValidationJob = (jobId: string): Promise<ValidationJob> =>
  request(`/api/v1/validation-jobs/${encodeURIComponent(jobId)}/retry`, { method: "POST" });
