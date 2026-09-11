// Typed wrappers around the explicit Rust command set. This is the only
// surface the UI uses to reach privileged host operations; it mirrors the
// commands registered in src-tauri/src/lib.rs.

import { invoke } from "@tauri-apps/api/core";
import { listen, type UnlistenFn } from "@tauri-apps/api/event";
import { openUrl } from "@tauri-apps/plugin-opener";
import { open } from "@tauri-apps/plugin-dialog";
import type { Project } from "./api/types";

export interface DockerProbe {
  docker_installed: boolean;
  docker_running: boolean;
  compose_available: boolean;
  docker_version: string | null;
  compose_version: string | null;
  install_url: string;
}

export interface ServiceStatus {
  service: string;
  state: string;
  health: string | null;
  status: string;
}

export const dockerProbe = (): Promise<DockerProbe> => invoke("docker_probe");

export const stackUp = (): Promise<void> => invoke("stack_up");
export const stackDown = (): Promise<void> => invoke("stack_down");
export const stackUpdate = (): Promise<void> => invoke("stack_update");
export const stackStatus = (): Promise<ServiceStatus[]> => invoke("stack_status");

export const loadUiState = <T = unknown>(): Promise<T | null> => invoke("load_ui_state");
export const saveUiState = (state: unknown): Promise<void> =>
  invoke("save_ui_state", { state });

// Open a URL in the user's default browser (used for the Docker install link).
export const openExternal = (url: string): Promise<void> => openUrl(url);

// --- Project import (host-side) ---

export interface FolderInfo {
  path: string;
  suggested_name: string;
  is_git_repo: boolean;
  has_commits: boolean;
  dirty: boolean;
  current_branch: string | null;
  estimated_files: number | null;
  estimated_bytes: number | null;
}

/** Native folder picker; returns the chosen path or null if cancelled. */
export const pickFolder = async (): Promise<string | null> => {
  const result = await open({ directory: true, multiple: false });
  return typeof result === "string" ? result : null;
};

export const inspectFolder = (path: string): Promise<FolderInfo> =>
  invoke("inspect_folder", { path });

export const importProject = (
  path: string,
  name: string,
  defaultBranch: string,
  recoveryPolicy: string,
): Promise<Project> =>
  invoke("import_project", {
    path,
    name,
    defaultBranch,
    recoveryPolicy,
  });

export const getProjectSource = (projectId: string): Promise<string | null> =>
  invoke("get_project_source", { projectId });

// --- Provider authentication / profiles (see docs/desktop-ipc.md) ---

export type ProfileStatus =
  | "not_configured"
  | "starting"
  | "waiting_for_browser"
  | "waiting_for_code"
  | "waiting_for_api_key"
  | "verifying"
  | "connected"
  | "expired"
  | "failed";

export type ProfileId = "codex-lead" | "codex-reviewer" | "claude-lead" | "claude-reviewer";

export interface ProfileDetail {
  message?: string;
  browserUrl?: string;
  deviceCode?: string;
}

export interface Profile {
  id: ProfileId;
  provider: "codex" | "claude";
  role: "lead" | "reviewer";
  status: ProfileStatus;
  detail?: ProfileDetail;
}

export type LoginMethod = "subscription" | "api_key";

export const listProfiles = (): Promise<Profile[]> => invoke("list_profiles");

export const beginLogin = (profileId: ProfileId, method: LoginMethod): Promise<void> =>
  invoke("begin_login", { profileId, method });

export const submitLoginCode = (profileId: ProfileId, code: string): Promise<void> =>
  invoke("submit_login_code", { profileId, code });

export const submitApiKey = (
  profileId: ProfileId,
  key: string,
  useForBothRoles?: boolean,
): Promise<void> => invoke("submit_api_key", { profileId, key, useForBothRoles });

export const cancelLogin = (profileId: ProfileId): Promise<void> =>
  invoke("cancel_login", { profileId });

export const verifyProfile = (profileId: ProfileId): Promise<Profile> =>
  invoke("verify_profile", { profileId });

export const disconnectProfile = (profileId: ProfileId): Promise<Profile> =>
  invoke("disconnect_profile", { profileId });

/** Subscribe to login-progress transitions. Returns an unlisten function. */
export const onLoginProgress = (
  handler: (p: { profileId: ProfileId; status: ProfileStatus; detail?: ProfileDetail }) => void,
): Promise<UnlistenFn> =>
  listen<{ profileId: ProfileId; status: ProfileStatus; detail?: ProfileDetail }>(
    "login_progress",
    (event) => handler(event.payload),
  );
