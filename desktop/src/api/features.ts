import { request } from "./client";
import type { CreateFeatureInput, Feature, Run, WorkflowEvent, Workspace } from "./types";

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
export const getFeatureEvents = (
  projectId: string,
  featureId: string,
): Promise<WorkflowEvent[]> =>
  request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}/events`,
  );

export const getWorkspace = (projectId: string, featureId: string): Promise<Workspace> =>
  request(
    `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}/workspace`,
  );

export const createFeature = (
  projectId: string,
  input: CreateFeatureInput,
): Promise<Feature> =>
  request(`/api/v1/projects/${encodeURIComponent(projectId)}/features`, {
    method: "POST",
    body: input,
  });
