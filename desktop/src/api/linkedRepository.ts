import { ApiError, request } from "./client";

/** How a project's main and its linked repository stand: commits each has
 * that the other lacks (SERVER_COMPLETE_PLAN.md, S2). */
export interface Comparison {
  state: "same" | "theirs_ahead" | "ours_ahead" | "diverged";
  theirs: number;
  ours: number;
}

/** A project's linked repository on GitHub or another git host. Its token
 * stays on the server. */
export interface LinkedRepository {
  url: string;
  branch: string;
  /** The project branch their commits wait on when both sides moved. */
  local_branch: string;
  has_token: boolean;
  automatic: boolean;
  pulled_at?: string;
  pushed_at?: string;
  last_error?: string;
  comparison?: Comparison;
  compare_error?: string;
}

const path = (projectId: string) =>
  `/api/v1/projects/${encodeURIComponent(projectId)}/linked-repository`;

/** The project's link, or null when it has none. */
export const getLinkedRepository = (projectId: string): Promise<LinkedRepository | null> =>
  request<LinkedRepository>(path(projectId)).catch((e) => {
    if (e instanceof ApiError && e.code === "not_linked") return null;
    throw e;
  });

export const linkRepository = (
  projectId: string,
  url: string,
  token: string,
  username = "",
): Promise<void> => request(path(projectId), { method: "PUT", body: { url, token, username } });

export const unlinkRepository = (projectId: string): Promise<void> =>
  request(path(projectId), { method: "DELETE" });

export const setSyncAutomatic = (projectId: string, automatic: boolean): Promise<void> =>
  request(`${path(projectId)}/automatic`, { method: "PUT", body: { automatic } });

export const bringInFromLinked = (projectId: string): Promise<Comparison> =>
  request(`${path(projectId)}/bring-in`, { method: "POST" });

export const pushToLinked = (projectId: string): Promise<Comparison> =>
  request(`${path(projectId)}/push`, { method: "POST" });
