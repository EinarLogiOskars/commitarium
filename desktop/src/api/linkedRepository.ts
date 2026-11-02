import { ApiError, request } from "./client";

/** A project's linked repository on GitHub or another git host
 * (SERVER_COMPLETE_PLAN.md, S2). Its token stays on the server. */
export interface LinkedRepository {
  url: string;
  branch: string;
  /** The project branch pulled commits wait on when main has its own. */
  local_branch: string;
  has_token: boolean;
  pulled_at?: string;
  pushed_at?: string;
}

/** What a pull did: brought commits into main, found none, or left them on
 * local_branch for Mayka to merge. */
export interface Pulled {
  outcome: "pulled" | "up_to_date" | "needs_merge";
  commits: number;
  local_branch: string;
}

/** The branch a push made there, and its pull request page. No branch
 * means main had nothing new. */
export interface Pushed {
  branch?: string;
  commits: number;
  pull_request_url?: string;
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

export const pullFromLinked = (projectId: string): Promise<Pulled> =>
  request(`${path(projectId)}/pull`, { method: "POST" });

export const pushToLinked = (projectId: string): Promise<Pushed> =>
  request(`${path(projectId)}/push`, { method: "POST" });
