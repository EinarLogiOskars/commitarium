import { request } from "./client";
import type { CreateFeatureInput, Feature, Run } from "./types";

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

export const createFeature = (
  projectId: string,
  input: CreateFeatureInput,
): Promise<Feature> =>
  request(`/api/v1/projects/${encodeURIComponent(projectId)}/features`, {
    method: "POST",
    body: input,
  });
