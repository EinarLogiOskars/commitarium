import { request } from "./client";
import type { EnvironmentRequest } from "./types";

// Approved system-package requests. An implementation lead blocked on a missing
// Debian package raises one; the user approves or rejects the exact list.
// Provisioning (image rebuild) is a native Tauri command, not a coordinator call.

export const listEnvironmentRequests = (projectId: string): Promise<{ requests: EnvironmentRequest[] }> =>
  request(`/api/v1/projects/${encodeURIComponent(projectId)}/environment-requests`);

export const getEnvironmentRequest = (id: string): Promise<EnvironmentRequest> =>
  request(`/api/v1/environment-requests/${encodeURIComponent(id)}`);

export const approveEnvironmentRequest = (id: string): Promise<EnvironmentRequest> =>
  request(`/api/v1/environment-requests/${encodeURIComponent(id)}/approve`, { method: "POST" });

export const rejectEnvironmentRequest = (id: string, reason: string): Promise<EnvironmentRequest> =>
  request(`/api/v1/environment-requests/${encodeURIComponent(id)}/reject`, {
    method: "POST",
    body: { reason },
  });

// requested/approved/provisioning/failed still need the user; ready/rejected are
// terminal. `approved` and `failed` are actionable because provisioning (or a
// retry) is a native command the renderer must invoke.
export const ENV_ACTIVE: ReadonlySet<EnvironmentRequest["status"]> = new Set([
  "requested",
  "approved",
  "provisioning",
  "failed",
]);
