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
  EnvironmentProvisionResult,
  MergePolicy,
  Project,
  ProjectDeletionResult,
  RecoveryPolicy,
  ServiceEvidence,
  ValidationRunResult,
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

/** Resolves to a warning for the user when the stack is up but needs attention. */
export const stackUp = (): Promise<string | null> => invoke("stack_up");
export const stackDown = (): Promise<void> => invoke("stack_down");
export const stackUpdate = (): Promise<string | null> => invoke("stack_update");
export const stackStatus = (): Promise<ServiceStatus[]> => invoke("stack_status");

// A fixed restricted, non-admin Forgejo account the user sets a password for, to
// browse the internal audit trail (repos, PRs, reviews) in a browser read-only.
export interface ForgejoViewerStatus {
  configured: boolean;
  username: "commitarium-viewer";
  loginUrl: string;
}

export const getForgejoViewerStatus = (): Promise<ForgejoViewerStatus> =>
  invoke("get_forgejo_viewer_status");

// The password travels only in this invocation + Forgejo's loopback API; it is
// never stored or returned.
export const configureForgejoViewer = (password: string): Promise<ForgejoViewerStatus> =>
  invoke("configure_forgejo_viewer", { password });

// --- Phase 5 desktop services ---

export interface DesktopSettings {
  notifications_configured: boolean;
  notifications_enabled: boolean;
  notify_attention: boolean;
  notify_failures: boolean;
  notify_auto_merges: boolean;
  exit_behavior: "keep_running" | "stop_stack";
}

export interface NativeNotification {
  event_id: string;
  kind: "attention" | "failure" | "auto_merge";
  title: string;
  body: string;
}

export interface NotificationDelivery {
  delivered: boolean;
  reason: "delivered" | "disabled" | "duplicate";
}

export interface DiskSpaceProbe {
  path: string;
  available_bytes: number;
  recommended_free_bytes: number;
  backup_ready: boolean;
}

export interface BackupResult {
  path: string;
  format_version: number;
  components: string[];
  credentials_included: boolean;
}

export interface BackupInspection extends BackupResult {
  app_version: string;
  created_at_unix_seconds: number;
}

export interface ExitConfirmationRequested {
  timeout_ms: number;
}

export const loadDesktopSettings = (): Promise<DesktopSettings> => invoke("load_desktop_settings");
export const saveDesktopSettings = (settings: DesktopSettings): Promise<DesktopSettings> =>
  invoke("save_desktop_settings", { settings });
export const confirmExit = (): Promise<boolean> => invoke("confirm_exit");
export const cancelExit = (): Promise<boolean> => invoke("cancel_exit");
export const onExitConfirmationRequested = (
  handler: (payload: ExitConfirmationRequested) => void,
): Promise<UnlistenFn> =>
  listen<ExitConfirmationRequested>("exit-confirmation-requested", (event) =>
    handler(event.payload),
  );
export const notifyAttention = (notification: NativeNotification): Promise<NotificationDelivery> =>
  invoke("notify_attention", { notification });
export const diskSpaceProbe = (): Promise<DiskSpaceProbe> => invoke("disk_space_probe");
export const createBackup = (destination: string): Promise<BackupResult> =>
  invoke("create_backup", { destination });
export const inspectBackup = (source: string): Promise<BackupInspection> =>
  invoke("inspect_backup", { source });
export const restoreBackup = (source: string): Promise<BackupResult> =>
  invoke("restore_backup", { source });

// --- Signed desktop application updates ---

export interface DesktopUpdateInfo {
  version: string;
  notes: string | null;
  published_at: string | null;
}

export type DesktopUpdateCheck =
  | { status: "up_to_date"; current_version: string }
  | { status: "available"; current_version: string; update: DesktopUpdateInfo };

export const checkDesktopUpdate = (): Promise<DesktopUpdateCheck> => invoke("check_desktop_update");

export interface UpdateBlockingWorkOrder {
  project_id: string;
  project_name: string;
  feature_id: string;
  feature_title: string;
  run_id: string;
  updated_at: string;
}

export type InstallDesktopUpdateResult =
  | { status: "blocked"; work_orders: UpdateBlockingWorkOrder[] }
  | { status: "installing"; version: string };

export type DesktopUpdateProgress =
  | {
      status: "downloading";
      version: string;
      downloaded_bytes: number;
      total_bytes: number | null;
    }
  | { status: "installing"; version: string }
  | { status: "restarting"; version: string };

export const installDesktopUpdate = (
  expectedVersion: string,
): Promise<InstallDesktopUpdateResult> => invoke("install_desktop_update", { expectedVersion });

export const onDesktopUpdateProgress = (
  handler: (payload: DesktopUpdateProgress) => void,
): Promise<UnlistenFn> =>
  listen<DesktopUpdateProgress>("desktop-update-progress", (event) => handler(event.payload));

export const loadUiState = <T = unknown>(): Promise<T | null> => invoke("load_ui_state");
export const saveUiState = (state: unknown): Promise<void> => invoke("save_ui_state", { state });

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

// Phase 4 native execution: the renderer supplies only the opaque coordinator
// ID; Tauri does the fixed Docker work (claim/build/run) itself.
export const runValidationJob = (jobId: string): Promise<ValidationRunResult> =>
  invoke("run_validation_job", { jobId });

/** Run Commitarium's preview check of a work order's commit (ADR-028): Tauri
 * claims it, builds and starts the app in isolation, and reports back. */
export const runPreviewCheck = (checkId: string): Promise<{ check_id: string; status: string }> =>
  invoke("run_preview_check", { checkId });

export const provisionEnvironmentRequest = (
  requestId: string,
): Promise<EnvironmentProvisionResult> => invoke("provision_environment_request", { requestId });

/** Delete coordinator-owned project state and its trusted local source mapping
 * as one resumable native operation. Reuse idempotencyKey on retry. */
export const deleteProject = (
  projectId: string,
  idempotencyKey: string,
  force = false,
): Promise<ProjectDeletionResult> => invoke("delete_project", { projectId, idempotencyKey, force });

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
  source: {
    sourceType: "git" | "plain_folder";
    path: string;
    createdByCommitarium: boolean;
  } | null;
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

export interface GitIdentityState {
  name: string | null;
  email: string | null;
}

export interface GitProviderProbe {
  provider: "github" | "gitlab" | "azure_devops";
  displayName: string;
  installed: boolean;
  authenticated: boolean;
  canCreate: boolean;
  account: string | null;
  host: string | null;
  detail: string | null;
  installUrl: string;
}

export interface CreateProjectRemoteRequest {
  projectId: string;
  provider: GitProviderProbe["provider"];
  namespace: string;
  repositoryName: string;
  visibility: "private" | "public" | "internal";
  azureProject?: string;
  idempotencyKey: string;
}

export interface ProjectRemoteResult {
  projectId: string;
  provider: string;
  repositoryPath: string;
  remoteName: string;
  remoteUrl: string;
  webUrl: string;
  defaultBranch: string;
  localCommitId: string;
  created: boolean;
  pushed: boolean;
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

export const getGitIdentity = (parentPath?: string): Promise<GitIdentityState> =>
  invoke("get_git_identity", { parentPath });

export const initializeProjectLocalRepository = (
  projectId: string,
  parentPath: string,
  folderName: string,
  commitMessage: string,
  authorName: string,
  authorEmail: string,
  idempotencyKey: string,
): Promise<ProjectSynchronizeResult> =>
  invoke("initialize_project_local_repository", {
    projectId,
    parentPath,
    folderName,
    commitMessage,
    authorName,
    authorEmail,
    idempotencyKey,
  });

export const probeGitProviders = (): Promise<GitProviderProbe[]> => invoke("probe_git_providers");

export const createProjectRemote = (
  request: CreateProjectRemoteRequest,
): Promise<ProjectRemoteResult> => invoke("create_project_remote", { request });

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

// --- Project previews (ADR-013) ---
// Runs the project's canonical head from its validated compose file (ADR-014).
// camelCase like the project sync types.

// The default branch head, or a session's working copy with its uncommitted
// changes (ADR-019).
export type PreviewTarget = { kind: "canonical" } | { kind: "session"; feature_id: string };

export type PreviewState = "starting" | "running" | "failed" | "stopped";

export interface PreviewUrl {
  service: string;
  url: string; // http://127.0.0.1:<host port>
  open: boolean;
}

export interface PreviewStatus {
  projectId: string;
  target: PreviewTarget;
  commitId: string;
  state: PreviewState;
  urls: PreviewUrl[]; // one per published port, filled once "running"
  error: string | null; // set when "failed"
  /** What each container did, when a preview failed after its containers
   * were created (ADR-028). */
  services?: ServiceEvidence[];
}

/** Open an app's own window (APPS_PLAN.md), or bring it to the front. */
export const openAppWindow = (projectId: string, title: string): Promise<void> =>
  invoke("open_app_window", { projectId, title });

/** Start a preview, replacing any running one for the project. Returns in
 * "starting"; progress arrives via onPreviewStatusChanged. */
export const startPreview = (
  projectId: string,
  target: PreviewTarget = { kind: "canonical" },
): Promise<PreviewStatus> => invoke("start_preview", { projectId, target });

/** Stop the preview; its data volumes are kept. */
export const stopPreview = (projectId: string, featureId?: string): Promise<void> =>
  invoke("stop_preview", { projectId, featureId });

/** Stop the preview if running and remove its data volumes. */
export const resetPreviewData = (projectId: string): Promise<void> =>
  invoke("reset_preview_data", { projectId });

export const getPreviewStatus = (
  projectId: string,
  featureId?: string,
): Promise<PreviewStatus | null> => invoke("get_preview_status", { projectId, featureId });

/** Last 400 lines of combined output: [preview] for validation and builds,
 * "<service> | " for service output. */
export const getPreviewLogs = (projectId: string, featureId?: string): Promise<string[]> =>
  invoke("get_preview_logs", { projectId, featureId });

export const onPreviewStatusChanged = (
  handler: (status: PreviewStatus) => void,
): Promise<UnlistenFn> =>
  listen<PreviewStatus>("preview-status-changed", (event) => handler(event.payload));

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
  invoke("preview_upstream_branch", {
    projectId,
    featureId,
    workOrderName,
    remoteName,
    branchName,
  });

/** Push the exact clean local handoff commit to a new remote branch. */
export const publishUpstreamBranch = (
  projectId: string,
  featureId: string,
  remoteName: string,
  branchName: string,
): Promise<UpstreamBranchResult> =>
  invoke("publish_upstream_branch", { projectId, featureId, remoteName, branchName });

// --- Agent logins (provider authentication; see docs/desktop-ipc.md) ---

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

/** An agent's ID (ADR-016): each agent has one login, used for both roles. */
export type ProfileId = string;

export interface ProfileDetail {
  message?: string;
  browserUrl?: string;
  deviceCode?: string;
}

export interface Profile {
  id: ProfileId;
  name: string;
  provider: "codex" | "claude";
  status: ProfileStatus;
  detail?: ProfileDetail;
}

export type LoginMethod = "subscription" | "api_key";

export const listProfiles = (): Promise<Profile[]> => invoke("list_profiles");

export const beginLogin = (profileId: ProfileId, method: LoginMethod): Promise<void> =>
  invoke("begin_login", { profileId, method });

export const submitLoginCode = (profileId: ProfileId, code: string): Promise<void> =>
  invoke("submit_login_code", { profileId, code });

export const submitApiKey = (profileId: ProfileId, key: string): Promise<void> =>
  invoke("submit_api_key", { profileId, key });

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

// --- Agent CLI updates ---
// The agents' provider CLIs stay on the newest release on their own: the
// desktop installs a newer one into the workers' shared CLI volume and
// restarts the workers once none of them is running a turn.

export interface CliStatus {
  provider: "codex" | "claude";
  /** The version installed for the workers. */
  installed: string;
  /** The version being updated to, unless the CLI is current. */
  target: string | null;
  state: "current" | "updating" | "waiting" | "behind";
  /** Whether the latest check reached the npm registry; when it did not, a
   * current CLI may still have a newer release, and the check is retried. */
  checked: boolean;
}

/** The CLI of each provider with a running worker, and any update in progress. */
export const agentCliStatus = (): Promise<CliStatus[]> => invoke("agent_cli_status");

/** Subscribe to CLI update progress. Returns an unlisten function. */
export const onAgentCliStatus = (handler: (statuses: CliStatus[]) => void): Promise<UnlistenFn> =>
  listen<CliStatus[]>("agent_cli_status", (event) => handler(event.payload));

/** Remove the previews of the project's sessions that are no longer open. */
export const pruneSessionPreviews = (projectId: string, openSessions: string[]): Promise<void> =>
  invoke("prune_session_previews", { projectId, openSessions });

// --- Coordinator freshness (development) ---
// Running from source, the coordinator image is built locally; after a pull
// it can be older than the code the desktop expects.

export type CoordinatorFreshness = "fresh" | "stale" | "unknown";

export const coordinatorFreshness = (): Promise<CoordinatorFreshness> =>
  invoke("coordinator_freshness");

/** Rebuild and restart the coordinator, unless an agent is running a turn ("busy"). */
export const rebuildCoordinator = (): Promise<"rebuilt" | "busy"> => invoke("rebuild_coordinator");

/** Whether this system can take a snapshot of the window (macOS for now). */
export const webviewSnapshotSupported = (): Promise<boolean> =>
  invoke<boolean>("webview_snapshot_supported");

/** The window's page as the webview draws it, framed apps included: a PNG.
 * It is the webview's own snapshot, so it needs no screen-recording
 * permission. */
export const webviewSnapshot = async (): Promise<Blob> =>
  new Blob([await invoke<ArrayBuffer>("webview_snapshot")], { type: "image/png" });
