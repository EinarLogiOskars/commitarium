// Typed wrappers around the explicit Rust command set. This is the only
// surface the UI uses to reach privileged host operations; it mirrors the
// commands registered in src-tauri/src/lib.rs.

import { invoke } from "@tauri-apps/api/core";
import { listen, type UnlistenFn } from "@tauri-apps/api/event";
import { openUrl } from "@tauri-apps/plugin-opener";
import { open } from "@tauri-apps/plugin-dialog";
import type {
  AgentModels,
  AgentProviders,
  AutonomyPolicy,
  DialogueLimits,
  MergePolicy,
  Project,
  ProjectDeletionResult,
  RecoveryPolicy,
} from "./api/types";

export interface DockerProbe {
  docker_installed: boolean;
  docker_running: boolean;
  compose_available: boolean;
  docker_launchable: boolean;
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
export const launchDockerDesktop = (): Promise<void> => invoke("launch_docker_desktop");

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

export interface ImportProjectDefaults {
  agent_providers?: AgentProviders;
  agent_models?: AgentModels;
  autonomy_policy?: AutonomyPolicy;
  merge_policy?: MergePolicy;
  dialogue_limits?: DialogueLimits;
}

export const importProject = (
  path: string,
  name: string,
  defaultBranch: string,
  recoveryPolicy: RecoveryPolicy,
  defaults: ImportProjectDefaults = {},
): Promise<Project> =>
  invoke("import_project", {
    path,
    name,
    defaultBranch,
    recoveryPolicy,
    agentProviders: defaults.agent_providers,
    agentModels: defaults.agent_models,
    autonomyPolicy: defaults.autonomy_policy,
    mergePolicy: defaults.merge_policy,
    dialogueLimits: defaults.dialogue_limits,
  });

export const getProjectSource = (projectId: string): Promise<string | null> =>
  invoke("get_project_source", { projectId });

/** Delete coordinator-owned project state and its trusted local source mapping
 * as one resumable native operation. Reuse idempotencyKey on retry. */
export const deleteProject = (
  projectId: string,
  idempotencyKey: string,
  force = false,
): Promise<ProjectDeletionResult> =>
  invoke("delete_project", { projectId, idempotencyKey, force });

// --- Handoff: completed work → host (see docs/desktop-ipc.md) ---
// NOTE: SynchronizeResult / FolderSynchronizeResult serialize snake_case
// (no serde rename on the Rust structs); the upstream types are camelCase.

export interface SynchronizeResult {
  project_id: string;
  feature_id: string;
  repository_path: string;
  target_branch: string;
  local_commit_id: string;
  created: boolean;
}

export interface FolderSynchronizeResult {
  project_id: string;
  feature_id: string;
  folder_path: string;
  result_tree_id: string;
  created: boolean;
}

export type UpstreamBranchStatus =
  | "selection_required"
  | "ready"
  | "published"
  | "already_published"
  | "branch_conflict"
  | "authentication_required"
  | "remote_unavailable";

export interface UpstreamRemote {
  name: string;
  displayLocation: string; // credentials removed
}

export interface UpstreamBranchResult {
  projectId: string;
  featureId: string;
  repositoryPath: string;
  localCommitId: string;
  remotes: UpstreamRemote[];
  selectedRemote?: string;
  branchName: string;
  status: UpstreamBranchStatus;
  created: boolean;
  detail?: string;
}

// --- Project-level handoff: canonical project → host (see docs/desktop-ipc.md) ---
// These are camelCase (unlike the feature-level Synchronize results).

export interface CompletedProjectSyncItem {
  featureId: string;
  title: string;
  baseCommitId: string;
  mergeCommitId: string;
  mergedAt: string;
}

export interface ProjectTargetState {
  watermarkCommitId: string | null;
  localCommitId: string | null;
  unsyncedFeatures: CompletedProjectSyncItem[];
}

export interface ProjectSyncState {
  projectId: string;
  source: { sourceType: "git" | "plain_folder"; path: string } | null;
  canonical: { defaultBranch: string; headCommitId: string };
  local: ProjectTargetState;
  upstreams: Array<{
    remoteName: string;
    displayLocation: string;
    branchName: string | null;
    watermarkCommitId: string | null;
    unsyncedFeatures: CompletedProjectSyncItem[];
  }>;
}

export interface ProjectSynchronizeResult {
  projectId: string;
  sourceType: "git" | "plain_folder";
  sourcePath: string;
  canonicalCommitId: string;
  targetBranch: string | null;
  localCommitId: string | null;
  resultTreeId: string;
  created: boolean;
}

export interface ProjectUpstreamResult {
  projectId: string;
  repositoryPath: string;
  canonicalCommitId: string;
  localCommitId: string;
  remotes: UpstreamRemote[];
  selectedRemote: string | null;
  branchName: string;
  status: UpstreamBranchStatus;
  created: boolean;
  detail: string | null;
}

export const getProjectSyncState = (projectId: string): Promise<ProjectSyncState> =>
  invoke("get_project_sync_state", { projectId });

export const synchronizeProjectLocally = (
  projectId: string,
  commitMessage: string,
): Promise<ProjectSynchronizeResult> =>
  invoke("synchronize_project_locally", { projectId, commitMessage });

export const previewProjectUpstreamBranch = (
  projectId: string,
  remoteName?: string,
  branchName?: string,
): Promise<ProjectUpstreamResult> =>
  invoke("preview_project_upstream_branch", { projectId, remoteName, branchName });

export const publishProjectUpstreamBranch = (
  projectId: string,
  remoteName: string,
  branchName: string,
): Promise<ProjectUpstreamResult> =>
  invoke("publish_project_upstream_branch", { projectId, remoteName, branchName });

/** Sync an approved feature into the user's local git repo as one clean commit. */
export const synchronizeFeatureLocally = (
  projectId: string,
  featureId: string,
  commitMessage: string,
): Promise<SynchronizeResult> =>
  invoke("synchronize_feature_locally", { projectId, featureId, commitMessage });

/** Sync an approved feature into a non-git folder. */
export const synchronizeFeatureToFolder = (
  projectId: string,
  featureId: string,
): Promise<FolderSynchronizeResult> =>
  invoke("synchronize_feature_to_folder", { projectId, featureId });

/** Read-only probe: list remotes, suggest a branch, check the selected branch. */
export const previewUpstreamBranch = (
  projectId: string,
  featureId: string,
  workOrderName: string,
  remoteName?: string,
  branchName?: string,
): Promise<UpstreamBranchResult> =>
  invoke("preview_upstream_branch", { projectId, featureId, workOrderName, remoteName, branchName });

/** Push the exact clean local handoff commit to a new remote branch. */
export const publishUpstreamBranch = (
  projectId: string,
  featureId: string,
  remoteName: string,
  branchName: string,
): Promise<UpstreamBranchResult> =>
  invoke("publish_upstream_branch", { projectId, featureId, remoteName, branchName });

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
