// Typed wrappers around the explicit Rust command set and the host's HTTP API.
// This is the only surface the UI uses to reach privileged host operations; it
// mirrors the commands registered in src-tauri/src/lib.rs and the routes in
// src-tauri/host/src/server.rs.

import { connectedServer, TAILSCALE_HINT, throughTailscale } from "./connection";
import { invoke } from "@tauri-apps/api/core";
import { listen, type UnlistenFn } from "@tauri-apps/api/event";
import { fetch } from "@tauri-apps/plugin-http";
import { openUrl } from "@tauri-apps/plugin-opener";
import { open } from "@tauri-apps/plugin-dialog";
import type {
  AgentModels,
  AgentProviders,
  RunningWorkOrder,
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

export const launchDockerDesktop = (): Promise<void> => invoke("launch_docker_desktop");

/** Starts the stack and waits until it is ready. Resolves to a warning for the
 * user when it is up but needs attention. */
export const stackStart = (): Promise<string | null> => invoke("stack_start");
/** Resolves to a warning for the user when the stack is up but needs attention. */
export const stackUp = (): Promise<string | null> => invoke("stack_up");
export const stackDown = (): Promise<void> => invoke("stack_down");
export const stackUpdate = (): Promise<string | null> => invoke("stack_update");
/** Calls the host (ADR-040). Each call asks for the connection, because a host
 * that went away comes back on another port. */
const hostGet = async <T>(path: string): Promise<T> => {
  const { url, token } = await invoke<{ url: string; token: string }>("host_connection");
  const response = await fetch(url + path, {
    headers: { Authorization: `Bearer ${token}` },
  });
  return hostAnswer<T>(response);
};

/** The host's JSON answer. A host that is restarting, or a server in the
 * middle of an update, may answer with something else for a moment: that is
 * said in words, not as a parsing error. */
const hostAnswer = async <T>(response: Response): Promise<T> => {
  // The bridge answers an empty 502 when the server itself does not.
  if (response.status === 502 && connectedServer()) {
    const hint = throughTailscale() ? ` ${TAILSCALE_HINT}` : "";
    throw new Error(`Commitarium can't reach the server right now.${hint}`);
  }
  const text = await response.text();
  let body: { error?: string } | null;
  try {
    body = JSON.parse(text);
  } catch {
    throw new Error(
      "The server didn't answer properly. It may be restarting; try again in a moment.",
    );
  }
  if (!response.ok) throw new Error(body?.error ?? `host error ${response.status}`);
  return body as T;
};

const hostPost = async <T>(path: string, payload?: unknown): Promise<T> => {
  const { url, token } = await invoke<{ url: string; token: string }>("host_connection");
  const response = await fetch(url + path, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify(payload ?? null),
  });
  return hostAnswer<T>(response);
};

// --- Deployments and publishing (PUBLISHING_PLAN.md) ---

export interface Deploy {
  commit_id: string;
  /** Seconds since the Unix epoch. */
  deployed_at: number;
}

export interface Deployment {
  project_id: string;
  current: Deploy | null;
  /** Newest first. */
  history: Deploy[];
  status: PreviewStatus | null;
}

export type Access = "private" | "public" | "company";

/** An address under one of the server's domains; the empty name is the
 * domain itself. */
export interface AppAddress {
  domain: string;
  name: string;
}

export interface PublishedApp {
  project_id: string;
  /** Its main address's domain; empty only on the private network. */
  domain: string;
  /** Its main address's name; empty for the domain itself (a website). */
  name: string;
  access: Access;
  /** Its main address. */
  url: string;
  /** Every address, its main one first. */
  urls: string[];
  /** For an app at a bare domain: whether www. sends people to it. */
  www: boolean;
  /** Its extra addresses, which reach the same running app. */
  aliases: (AppAddress & { url: string })[];
  live: boolean;
  /** For company sign-in: the groups that may open it; none means anyone at the company. */
  groups: string[];
  /** Whether the apps page lists it. */
  listed: boolean;
  /** What is wrong with its records, when an address does not point here. */
  record_problem: string | null;
}

/** The apps page at apps.<domain> (APPS_PAGE_PLAN.md), when it is on. */
export interface AppsPage {
  /** Its address name: apps, unless the owner chose another. */
  name: string;
  access: Access;
  url: string;
}

/** The company's sign-in provider, as a server shows it: never its secret. */
export interface SignInProvider {
  issuer: string;
  client_id: string;
  scopes: string;
  groups_claim: string;
  /** The address to register with the provider; null until the domain is set. */
  redirect_url: string | null;
  /** Signs a person out of every app on the server; null until the domain is set. */
  sign_out_url: string | null;
}

export interface ConnectSignIn {
  issuer: string;
  client_id: string;
  client_secret: string;
  scopes?: string;
  groups_claim?: string;
  /** The domain it signs people in for; the first when left out. */
  domain?: string;
}

/** Who reaches the apps (HOMELAB_PLAN.md): anyone on the internet at the
 * server's domain, or only the owner's devices, on their Tailscale network. */
export type Reach = "internet" | "private";
/** How the domain's DNS points at the server: one wildcard record, or a
 * record for each address in use. */
export type Records = "wildcard" | "each_name";
/** Where the owner said the server is, asked once. */
export type Placement = "home" | "cloud" | "other";

/** Tailscale on the server, in the steps the owner goes through. */
export interface TailscaleStatus {
  state: "starting" | "needs_login" | "needs_https" | "ready" | "system_signed_out";
  login_url: string | null;
  /** Its name on the tailnet, such as homelab.tail1234.ts.net. */
  name: string | null;
  /** Its Tailscale address, such as 100.101.102.103. */
  address: string | null;
  /** Whether Commitarium runs it, or the server does. */
  own: boolean;
  /** Where the owner turns on HTTPS certificates. */
  https_settings: string;
}

/** One of the server's domains, with what is set up on it. */
export interface DomainShown {
  name: string;
  records: Records;
  /** Its company sign-in, when one is connected. */
  sign_in: SignInProvider | null;
  /** Whether that sign-in is this server's authentik. */
  authentik: boolean;
  /** Its apps page, when it is on. */
  apps_page: AppsPage | null;
}

export interface PublishingOverview {
  /** Every domain, the first one first; empty on a server without one. */
  domains: DomainShown[];
  /** The first domain, and below its sign-in, apps page and records, as
   * servers before several domains had them. */
  domain: string | null;
  server_address: string | null;
  apps: PublishedApp[];
  sign_in: SignInProvider | null;
  authentik: AuthentikStatus;
  /** The first domain's apps page; in Homelab, the one on the private network. */
  apps_page: AppsPage | null;
  reach: Reach;
  tailscale: TailscaleStatus | null;
  records: Records;
  placement: Placement | null;
}

/** authentik on the server, for a company without a sign-in provider. */
export interface AuthentikStatus {
  installed: boolean;
  running: boolean;
  admin_url: string | null;
  admin_user: string;
  /** The server's memory; null where the system does not say. */
  memory_total_bytes: number | null;
  /** What authentik needs for itself. */
  memory_needed_gb: number;
}

export const deployments = (): Promise<Deployment[]> => hostGet("/v1/deployments");
/** The default branch's head unless a commit is named. */
export const deploy = (projectId: string, commitId?: string): Promise<PreviewStatus> =>
  hostPost("/v1/deployments/deploy", { project_id: projectId, commit_id: commitId ?? null });
export const stopDeployment = (projectId: string): Promise<void> =>
  hostPost("/v1/deployments/stop", { project_id: projectId });
/** A server older than this app leaves out what it does not have yet, so
 * those parts read as off, not missing. One from before several domains
 * reads as its one domain, and every app at it. */
export const publishing = async (): Promise<PublishingOverview> =>
  withDefaults(await hostGet<Partial<PublishingOverview>>("/v1/publishing"));

export function withDefaults(found: Partial<PublishingOverview>): PublishingOverview {
  const domain = found.domain ?? null;
  const records = found.records ?? "wildcard";
  const sign_in = found.sign_in ?? null;
  const apps_page = found.apps_page ?? null;
  const authentik = found.authentik ?? {
    installed: false,
    running: false,
    admin_url: null,
    admin_user: "akadmin",
    memory_total_bytes: null,
    memory_needed_gb: 3,
  };
  const domains =
    found.domains ??
    (domain
      ? [
          {
            name: domain,
            records,
            sign_in,
            // Before several domains, a connected provider was authentik
            // exactly when the server ran it.
            authentik: sign_in !== null && authentik.installed,
            apps_page: found.reach === "private" ? null : apps_page,
          },
        ]
      : []);
  const first = domains[0]?.name ?? "";
  return {
    ...found,
    domains,
    domain,
    server_address: found.server_address ?? null,
    apps: (found.apps ?? []).map((app) => ({
      ...app,
      domain: app.domain ?? first,
      urls: app.urls ?? (app.url ? [app.url] : []),
      www: app.www ?? false,
      aliases: app.aliases ?? [],
      listed: app.listed ?? true,
    })),
    sign_in,
    authentik,
    apps_page,
    reach: found.reach ?? "internet",
    tailscale: found.tailscale ?? null,
    records,
    placement: found.placement ?? null,
  };
}

/** Only the named domain, so a server from before several domains, which
 * knows one, keeps working. */
const onDomain = (domain?: string | null) => (domain ? { domain } : {});

/** Sets the first domain, or renames it: its apps move with it. */
export const setDomain = (domain: string, records: Records = "wildcard"): Promise<void> =>
  hostPost("/v1/publishing/domain", { domain, records });
/** Adds another domain; refused until its wildcard record points here, with
 * one. */
export const addDomain = (domain: string, records: Records = "wildcard"): Promise<void> =>
  hostPost("/v1/publishing/add-domain", { domain, records });
/** Lets go of a domain; refused, with why, while apps, the apps page,
 * sign-in or authentik use it. */
export const removeDomain = (domain?: string): Promise<void> =>
  hostPost("/v1/publishing/remove-domain", onDomain(domain));
export const setPlacement = (placement: Placement): Promise<void> =>
  hostPost("/v1/publishing/placement", { placement });
/** Tells the Server settings that publishing changed, so each part reads it again. */
export const PUBLISHING_CHANGED = "commitarium:publishing-changed";

/** Where an app is published: each part left out stays as it is, or, for a
 * new app, the first domain with www on. */
export interface Placing {
  domain?: string;
  /** For a bare-domain app: whether www. sends people to it. */
  www?: boolean;
  aliases?: AppAddress[];
}

/** A private app needs a password the first time; the user chooses it. */
export const publishApp = (
  projectId: string,
  name: string,
  access: Access,
  password?: string,
  placing: Placing = {},
): Promise<{ url: string; urls?: string[] }> =>
  hostPost("/v1/publishing/publish", {
    project_id: projectId,
    name,
    access,
    password: password || null,
    ...onDomain(placing.domain),
    ...(placing.www === undefined ? {} : { www: placing.www }),
    ...(placing.aliases === undefined ? {} : { aliases: placing.aliases }),
  });
export const setAppPassword = (projectId: string, password: string): Promise<void> =>
  hostPost("/v1/publishing/password", { project_id: projectId, password });
/** Making an app private again uses the password it had. */
export const setAppAccess = (projectId: string, access: Access): Promise<void> =>
  hostPost("/v1/publishing/access", { project_id: projectId, access });
/** Null for a password set before passwords were kept. */
export const revealAppPassword = (projectId: string): Promise<string | null> =>
  hostPost("/v1/publishing/reveal", { project_id: projectId });
export const unpublishApp = (projectId: string): Promise<void> =>
  hostPost("/v1/publishing/unpublish", { project_id: projectId });
/** The server checks the provider answers before keeping it. */
export const connectSignIn = ({ domain, ...provider }: ConnectSignIn): Promise<SignInProvider> =>
  hostPost("/v1/publishing/sign-in", { ...provider, ...onDomain(domain) });
/** Takes minutes: authentik's images, then its first start. On a domain
 * other than the one it lives at, it connects that domain to it. */
export const installAuthentik = (domain?: string): Promise<SignInProvider> =>
  hostPost("/v1/publishing/authentik/install", onDomain(domain));
export const authentikPassword = (): Promise<string> =>
  hostPost("/v1/publishing/authentik/password");
/** Refused while an app still uses company sign-in. */
export const disconnectSignIn = (domain?: string): Promise<void> =>
  hostPost("/v1/publishing/sign-in/disconnect", onDomain(domain));
export const setAppGroups = (projectId: string, groups: string[]): Promise<void> =>
  hostPost("/v1/publishing/groups", { project_id: projectId, groups });
/** Turns a domain's apps page on with who may open it, or off with null. A
 * password page needs a password the first time. */
export const setAppsPage = (
  access: Access | null,
  password?: string,
  name?: string,
  domain?: string,
): Promise<void> =>
  hostPost("/v1/publishing/apps-page", {
    access,
    password: password || null,
    name: name || null,
    ...onDomain(domain),
  });
/** Every app's address changes: the app asks first. */
export const setReach = (reach: Reach): Promise<void> =>
  hostPost("/v1/publishing/reach", { reach });
/** Rejects with why the apps page could not live at this name. */
export const checkAppsPageName = (name: string, domain?: string): Promise<void> =>
  hostPost("/v1/publishing/apps-page/check", { name, ...onDomain(domain) });
export const revealAppsPagePassword = (domain?: string): Promise<string | null> =>
  hostPost("/v1/publishing/apps-page/reveal", onDomain(domain));
export const setListed = (projectId: string, listed: boolean): Promise<void> =>
  hostPost("/v1/publishing/listed", { project_id: projectId, listed });

// --- Remote mode (ADR-040) ---

export interface ConnectionMode {
  /** Null on a first launch, which asks. */
  mode: "local" | "remote" | null;
  server: string | null;
}

export const connectionMode = (): Promise<ConnectionMode> => invoke("connection_mode");
export const chooseThisComputer = (): Promise<void> => invoke("use_this_computer");
/** Pairs with the server in a line from `commitarium-host pair`. */
export const connectToServer = (line: string): Promise<void> =>
  invoke("connect_to_server", { line });
/** Shows a paired server; the window reloads after. */
export const showServer = (fingerprint: string): Promise<void> =>
  invoke("use_server", { fingerprint });

export interface PairedServer {
  fingerprint: string;
  address: string;
  /** The user's name for it, else its address. */
  name: string;
  /** Where it was reached before it moved, if it did: the way back. */
  previous_address: string | null;
}

/** This computer and the paired servers; `active` is null for this computer. */
export interface Places {
  servers: PairedServer[];
  active: string | null;
}

export const places = (): Promise<Places> => invoke("places");
/** An empty name goes back to the address. */
export const renameServer = (fingerprint: string, name: string): Promise<void> =>
  invoke("rename_server", { fingerprint, name });
export const forgetServer = (fingerprint: string): Promise<void> =>
  invoke("forget_server", { fingerprint });
/** Reaches a paired server at another address from now on, once it answers
 * there, such as its Tailscale address. */
export const moveServer = (fingerprint: string, address: string): Promise<void> =>
  invoke("move_server", { fingerprint, address });

export const bridgeConnection = (): Promise<{ coordinator_url: string | null; token: string }> =>
  invoke("host_connection");

/** How an app window reaches the coordinator on a server. Null on this computer. */
export const coordinatorConnection = (): Promise<{
  coordinator_url: string;
  token: string;
} | null> => invoke("coordinator_connection");

export interface HostVersion {
  version: string;
  api_version: number;
}
export interface HostUpdate {
  current: string;
  latest: string;
  available: boolean;
}
export interface PairedDevice {
  fingerprint: string;
  name: string;
  role: string;
  paired_at: number;
}
export interface PairingOpened {
  code: string;
  port: number;
  seconds: number;
}

export const hostVersion = (): Promise<HostVersion> => hostGet("/v1/version");
export const hostUpdate = (): Promise<HostUpdate> => hostGet("/v1/host/update");
export const installHostUpdate = (): Promise<null> => hostPost("/v1/host/update");
export const pairedDevices = (): Promise<PairedDevice[]> => hostGet("/v1/devices");
export const openPairing = (): Promise<PairingOpened> => hostPost("/v1/pairing/open");
export const revokeDevice = (device: string): Promise<null> =>
  hostPost("/v1/devices/revoke", { device });

export const stackStatus = (): Promise<ServiceStatus[]> => hostGet("/v1/stack/status");
export const dockerProbe = (): Promise<DockerProbe> => hostGet("/v1/docker/probe");
export const diskSpaceProbe = (): Promise<DiskSpaceProbe> => hostGet("/v1/disk/space");

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
  /** What agents do on this computer, whichever place is shown. */
  running: RunningWorkOrder[];
  server_shown: boolean;
}

export const loadDesktopSettings = (): Promise<DesktopSettings> => invoke("load_desktop_settings");
export const saveDesktopSettings = (settings: DesktopSettings): Promise<DesktopSettings> =>
  invoke("save_desktop_settings", { settings });
export const confirmExit = (): Promise<boolean> => invoke("confirm_exit");
export const cancelExit = (): Promise<boolean> => invoke("cancel_exit");
/** Tells the app the quit question is on screen; without it, quitting goes on. */
export const exitPromptShown = (): Promise<void> => invoke("exit_prompt_shown");

export const onExitConfirmationRequested = (
  handler: (payload: ExitConfirmationRequested) => void,
): Promise<UnlistenFn> =>
  listen<ExitConfirmationRequested>("exit-confirmation-requested", (event) =>
    handler(event.payload),
  );
export const notifyAttention = (notification: NativeNotification): Promise<NotificationDelivery> =>
  invoke("notify_attention", { notification });
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

export const restartApp = (): Promise<void> => invoke("restart_app");

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
/** Forgejo's own addresses: on a server, the bridge stands in for them. */
const FORGEJO_HERE = /^http:\/\/(127\.0\.0\.1|localhost):3001(?=\/|$)/;

export const openExternal = async (url: string): Promise<void> => {
  if (connectedServer() && FORGEJO_HERE.test(url)) {
    // The server's Forgejo reaches the browser through the bridge (S4).
    const base = await invoke<string | null>("forgejo_address");
    if (base) return openUrl(url.replace(FORGEJO_HERE, base));
  }
  return openUrl(url);
};

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
  /** Commits on the user's computer that Commitarium has not seen. */
  localCommits: number;
  uncommittedFiles: number;
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

export interface LocalChanges {
  headCommitId: string;
  commits: number;
  files: string[];
  uncommittedFiles: number;
  patch: string;
}

/** The commits on the user's computer that Commitarium has not seen. */
export const prepareLocalChanges = (projectId: string): Promise<LocalChanges> =>
  invoke("prepare_local_changes", { projectId });

export const recordLocalChangesBroughtIn = (projectId: string, commitId: string): Promise<void> =>
  invoke("record_local_changes_brought_in", { projectId, commitId });

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
export type PreviewTarget =
  | { kind: "canonical" }
  | { kind: "session"; feature_id: string }
  /** An app's deployment (PUBLISHING_PLAN.md), never a preview. */
  | { kind: "deployment"; commit_id?: string | null };

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

/** Open an app's own window (APPS_PLAN.md), or bring it to the front: the
 * real app's, or with `preview` its preview's, which says it is a test copy
 * (HELPERS_PLAN.md H5). On a server every window shows the preview. */
export const openAppWindow = (
  projectId: string,
  title: string,
  options: { preview?: boolean } = {},
): Promise<void> => invoke("open_app_window", { projectId, title, preview: !!options.preview });

/** How the user opened an app's real app last. */
export type RealAppOpened = "window" | "browser";

/** What there is to open of a project's real app (HELPERS_PLAN.md H5). */
export interface RealApp {
  /** On a server the real app is the deployment, opened in a browser. */
  on_server: boolean;
  /** On a server: whether the app is deployed. */
  deployed: boolean;
  /** Its address for the browser: on this computer while it runs, on a
   * server once it has one. */
  address: string | null;
  last: RealAppOpened | null;
}

export const realApp = (projectId: string): Promise<RealApp> => invoke("real_app", { projectId });

/** The real app's state on this computer (HELPERS_PLAN.md H7): the
 * project's deployment, a stack apart from the preview with data of its
 * own; null when it is not running. Its events share
 * onPreviewStatusChanged, with target kind "deployment". */
export const getRealAppStatus = (projectId: string): Promise<PreviewStatus | null> =>
  invoke("real_app_status", { projectId });

/** Start the real app on this computer, or move it to the default branch's
 * head, keeping its data. One already up at the head is left running. */
export const startRealApp = (projectId: string): Promise<PreviewStatus> =>
  invoke("start_real_app", { projectId });

/** Opens the real app in the browser, at the address the host knows for it. */
export const openRealAppInBrowser = (projectId: string): Promise<void> =>
  invoke("open_real_app_in_browser", { projectId });

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
export const onStackProgress = (handler: (step: string) => void): Promise<UnlistenFn> =>
  listen<{ step: string }>("stack_progress", (event) => handler(event.payload.step));

/** A backup kept on a server (SERVER_COMPLETE_PLAN.md, S1). */
export interface ServerBackup {
  name: string;
  created_at_unix_seconds: number;
  app_version: string;
  /** Bytes on disk. */
  size: number;
}

export const serverBackups = (): Promise<ServerBackup[]> => hostGet("/v1/backups");
export const createServerBackup = (): Promise<BackupResult> => hostPost("/v1/backups/create");
export const restoreServerBackup = (name: string): Promise<BackupResult> =>
  hostPost("/v1/backups/restore", { name });

/** What a server's backups are doing: its stack is stopped meanwhile. */
export type BackupUnderway = "backup" | "restore" | null;

export const backupUnderway = async (): Promise<BackupUnderway> =>
  (await hostGet<{ underway: BackupUnderway }>("/v1/backups/underway")).underway;

export const onBackupProgress = (handler: (step: string) => void): Promise<UnlistenFn> =>
  listen<{ step: string }>("backup_progress", (event) => handler(event.payload.step));

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
