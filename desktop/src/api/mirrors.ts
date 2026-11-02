import { request } from "./client";

/** A git remote Forgejo keeps a project's copy up to date on
 * (SERVER_COMPLETE_PLAN.md, S2). Its token stays in Forgejo. */
export interface PushMirror {
  name: string;
  remote_url: string;
  last_update?: string;
  last_error?: string;
}

const mirrorsPath = (projectId: string) =>
  `/api/v1/projects/${encodeURIComponent(projectId)}/mirrors`;

export const listMirrors = (projectId: string): Promise<{ mirrors: PushMirror[] }> =>
  request(mirrorsPath(projectId));

export const addMirror = (
  projectId: string,
  remoteUrl: string,
  token: string,
  username?: string,
): Promise<void> =>
  request(mirrorsPath(projectId), {
    method: "POST",
    body: { remote_url: remoteUrl, token, username: username ?? "" },
  });

export const pushMirrorsNow = (projectId: string): Promise<void> =>
  request(`${mirrorsPath(projectId)}/sync`, { method: "POST" });

export const removeMirror = (projectId: string, name: string): Promise<void> =>
  request(`${mirrorsPath(projectId)}/${encodeURIComponent(name)}`, { method: "DELETE" });
