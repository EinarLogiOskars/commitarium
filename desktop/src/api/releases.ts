import { request } from "./client";

// Releasing an app to the marketplace (MARKETPLACE_PLAN.md, slice 4).

export interface ReleaseVersion {
  version: string;
  commit: string;
  source_commit: string;
  released_at: string;
  released_by: string;
  repository_url: string;
}

export interface ReleaseStatus {
  latest: ReleaseVersion | null;
  /** The app changed since its latest release, or was never released. */
  unreleased: boolean;
}

const releasePath = (projectId: string) =>
  `/api/v1/projects/${encodeURIComponent(projectId)}/release`;

export const getReleaseStatus = (projectId: string) =>
  request<ReleaseStatus>(releasePath(projectId));

export const releaseApp = (projectId: string) =>
  request<ReleaseVersion>(releasePath(projectId), { method: "POST" });
