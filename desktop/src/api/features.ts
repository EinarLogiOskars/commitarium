import { request } from "./client";
import type {
  CreateFeatureInput,
  Feature,
  FeatureArtifact,
  FeatureArtifactKind,
  FeatureUsage,
  WorkOrderAssistantSession,
  HandoffBriefDocument,
  PreviewCheck,
  Run,
  ServiceEvidence,
  WorkflowEvent,
  Workspace,
} from "./types";

export const listFeatures = (projectId: string): Promise<Feature[]> =>
  request(`/api/v1/projects/${encodeURIComponent(projectId)}/features`);

export const getFeature = (projectId: string, featureId: string): Promise<Feature> =>
  request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}`,
  );

export const listFeatureRuns = (projectId: string, featureId: string): Promise<Run[]> =>
  request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}/runs`,
  );

const featurePath = (projectId: string, featureId: string) =>
  `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}`;

/** Start (or return) the draft's clarification with the project assistant. */
export const startAssistant = (
  projectId: string,
  featureId: string,
): Promise<WorkOrderAssistantSession> =>
  request(`${featurePath(projectId, featureId)}/assistant`, { method: "POST" });

/** Throws ApiError code assistant_not_found before the conversation exists. */
export const getAssistant = (
  projectId: string,
  featureId: string,
): Promise<WorkOrderAssistantSession> => request(`${featurePath(projectId, featureId)}/assistant`);

export const replyToAssistant = (
  projectId: string,
  featureId: string,
  message: string,
  idempotencyKey: string,
): Promise<WorkOrderAssistantSession> =>
  request(`${featurePath(projectId, featureId)}/assistant/messages`, {
    method: "POST",
    body: { message },
    idempotencyKey,
  });

/** Accept the handoff brief: the draft becomes Ready. */
export const acceptBrief = (
  projectId: string,
  featureId: string,
  idempotencyKey: string,
): Promise<Feature> =>
  request(`${featurePath(projectId, featureId)}/accept`, { method: "POST", idempotencyKey });

/** Return a Ready work order to Draft for more clarification. */
export const reopenWorkOrder = (
  projectId: string,
  featureId: string,
  idempotencyKey: string,
): Promise<Feature> =>
  request(`${featurePath(projectId, featureId)}/reopen`, { method: "POST", idempotencyKey });

export const updateHandoffBrief = (
  projectId: string,
  featureId: string,
  expectedRevision: number,
  document: HandoffBriefDocument,
  idempotencyKey: string,
): Promise<FeatureArtifact<HandoffBriefDocument>> =>
  request(`${featurePath(projectId, featureId)}/artifacts/handoff_brief`, {
    method: "PUT",
    body: { expected_revision: expectedRevision, document },
    idempotencyKey,
  });

export const getFeatureUsage = (projectId: string, featureId: string): Promise<FeatureUsage> =>
  request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}/usage`,
  );

/** Durable workflow history — used for phase-boundary timestamps. */
export const getFeatureEvents = (projectId: string, featureId: string): Promise<WorkflowEvent[]> =>
  request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}/events`,
  );

// Read a durable feature artifact (goal_draft or implementation_plan). Caller
// supplies the document type. Throws ApiError code artifact_not_found when the
// kind exists but the agent hasn't produced it yet, or feature_not_found.
export const getFeatureArtifact = <T = unknown>(
  projectId: string,
  featureId: string,
  kind: FeatureArtifactKind,
): Promise<FeatureArtifact<T>> =>
  request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}/artifacts/${kind}`,
  );

export const getWorkspace = (projectId: string, featureId: string): Promise<Workspace> =>
  request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}/workspace`,
  );

export const createFeature = (projectId: string, input: CreateFeatureInput): Promise<Feature> =>
  request(`/api/v1/projects/${encodeURIComponent(projectId)}/features`, {
    method: "POST",
    body: input,
  });

export interface DeleteFeatureResult {
  project_id: string;
  feature_id: string;
  deleted: boolean;
  merged_changes_remain: boolean;
}

/** Set a started work order back to Ready to start over from its brief
 * (ADR-017): its runs stop, its branch and pull request are closed, and the
 * brief is kept. Safe to retry with the same key. */
export const returnToReady = (
  projectId: string,
  featureId: string,
  idempotencyKey: string,
): Promise<Feature> =>
  request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}/return-to-ready`,
    { method: "POST", idempotencyKey },
  );

/** Close a work order without merging it: started work returns to Ready
 * first (its runs stop, its branch and pull request close), then it is
 * cancelled. Its history stays. Safe to retry with the same key. */
export const closeWorkOrder = (
  projectId: string,
  featureId: string,
  idempotencyKey: string,
): Promise<Feature> =>
  request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}/close`,
    { method: "POST", idempotencyKey },
  );

/** Delete a work order and its isolated internal artifacts. Never touches the
 * default branch. A forced delete stops exact active attempts first. */
export const deleteFeature = (
  projectId: string,
  featureId: string,
  idempotencyKey?: string,
  force = false,
): Promise<DeleteFeatureResult> =>
  request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}${force ? "?force=true" : ""}`,
    { method: "DELETE", idempotencyKey },
  );

// --- Sessions (ADR-019): work orders of kind "session" ---

/** Create a session on the chosen agent and model (ADR-027). It starts at
 * once: its branch and pull request open and it waits for the first message. */
export const createSession = (
  projectId: string,
  choice: Pick<CreateFeatureInput, "agent_providers" | "agent_models">,
): Promise<Feature> => createFeature(projectId, { title: "", kind: "session", ...choice });

/** Start the session's next agent turn. Refused with session_busy while the
 * agent is still working. */
export const sendSessionTurn = (
  projectId: string,
  featureId: string,
  message: string,
  idempotencyKey: string,
): Promise<Run> =>
  request(`${featurePath(projectId, featureId)}/messages`, {
    method: "POST",
    idempotencyKey,
    body: { message },
  });

/** Ask the session's agent to commit the work for the merge gate. */
export const finishSession = (
  projectId: string,
  featureId: string,
  idempotencyKey: string,
): Promise<Run> =>
  request(`${featurePath(projectId, featureId)}/finish`, { method: "POST", idempotencyKey });

export const renameFeature = (
  projectId: string,
  featureId: string,
  title: string,
): Promise<Feature> =>
  request(featurePath(projectId, featureId), { method: "PATCH", body: { title } });

// --- Background preview setup ---

export interface PreviewSetup {
  /** The background work order setting the preview up, when one runs. */
  work_order?: Feature;
}

/** Set the preview up in the background: a quick change adds a compose file. */
export const startPreviewSetup = (projectId: string): Promise<PreviewSetup> =>
  request(`/api/v1/projects/${encodeURIComponent(projectId)}/preview-setup`, { method: "POST" });

/** Report a preview that failed to start, so a background fix can start.
 * Answers 409 preview_fixes_exhausted once the automatic fixes are used up. */
export const reportPreviewFailure = (
  projectId: string,
  error: string,
  services?: ServiceEvidence[],
): Promise<PreviewSetup> =>
  request(`/api/v1/projects/${encodeURIComponent(projectId)}/preview-setup/failed`, {
    method: "POST",
    body: { error, ...failureEvidence(services) },
  });

/** "Set up preview": fix the preview that fails, or set it up when there is
 * no compose file, in a work order that merges only once Commitarium checked
 * the app runs (ADR-028). Starts even when the automatic fixes are used up. */
export const setUpPreview = (
  projectId: string,
  error: string,
  services?: ServiceEvidence[],
): Promise<PreviewSetup> =>
  request(`/api/v1/projects/${encodeURIComponent(projectId)}/preview-setup/failed`, {
    method: "POST",
    body: { error, chosen: true, ...failureEvidence(services) },
  });

/** Tell the project's assistant that the preview failed to start, with what
 * each container did, so it can set the preview up itself. */
export const tellAssistantPreviewFailed = (
  projectId: string,
  error: string,
  services?: ServiceEvidence[],
): Promise<void> =>
  request(`/api/v1/projects/${encodeURIComponent(projectId)}/preview-failure`, {
    method: "POST",
    body: { error, ...failureEvidence(services) },
  });

const failureEvidence = (services?: ServiceEvidence[]) =>
  services && services.length > 0 ? { evidence: { stage: "start", services } } : {};

/** A work order's preview checks, newest first. */
export const listPreviewChecks = async (
  projectId: string,
  featureId: string,
): Promise<PreviewCheck[]> =>
  (await request<{ checks: PreviewCheck[] }>(`${featurePath(projectId, featureId)}/preview-checks`))
    .checks;
