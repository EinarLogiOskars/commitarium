import { request } from "./client";
import type {
  CreateFeatureInput,
  Feature,
  FeatureArtifact,
  FeatureArtifactKind,
  GoalDraftDocument,
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

// Replace the editable proposed goal with optimistic concurrency. A stale
// expected_revision throws ApiError code artifact_revision_conflict (reload
// first); invalid/oversized content throws invalid_feature_artifact.
export const updateGoalDraft = (
  projectId: string,
  featureId: string,
  expectedRevision: number,
  document: GoalDraftDocument,
  idempotencyKey: string,
): Promise<FeatureArtifact<GoalDraftDocument>> =>
  request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}/artifacts/goal_draft`,
    { method: "PUT", body: { expected_revision: expectedRevision, document }, idempotencyKey },
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
 * default branch. Refuses (409 feature_active) while a run is active. */
export const deleteFeature = (projectId: string, featureId: string): Promise<DeleteFeatureResult> =>
  request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}`,
    { method: "DELETE" },
  );
