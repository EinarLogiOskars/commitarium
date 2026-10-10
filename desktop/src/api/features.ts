import { request } from "./client";
import type {
  CreateFeatureInput,
  Feature,
  FeatureArtifact,
  FeatureArtifactKind,
  FeatureUsage,
  WorkOrderAssistantSession,
  HandoffBriefDocument,
  Run,
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
