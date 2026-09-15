// Domain types mirroring the coordinator contract (docs/coordinator-api.md).

export type RecoveryPolicy = "approval_required" | "automatic";

export type AgentProvider = "codex" | "claude";
export interface AgentProviders {
  lead: AgentProvider;
  reviewer: AgentProvider;
}

/** Exact model IDs per role (never floating aliases like "latest"). */
export interface AgentModels {
  lead: string;
  reviewer: string;
}

export type AgentRole = "lead" | "reviewer";

/** One selectable model from a provider+role catalog. */
export interface ModelInfo {
  id: string;
  display_name: string;
  default_reasoning_effort?: string;
  supported_reasoning_efforts?: string[];
}

/** The persisted last-successful model list for a provider+role worker. */
export interface ModelCatalog {
  provider: AgentProvider;
  role: AgentRole;
  models: ModelInfo[];
  fetched_at: string;
  last_error?: string;
}

export interface ModelsResponse {
  catalogs: ModelCatalog[];
}

export type MergePolicy = "require_user_approval" | "auto_after_gates";

// Whether the coordinator stops at each phase checkpoint for the user, or runs
// the phases through on its own. Mandatory waits (round cap, blocker, merge gate,
// clarification) still stop in both modes.
export type AutonomyPolicy = "review_each_phase" | "run_to_completion";

export interface DialogueLimits {
  planning_rounds: number;
  implementation_review_rounds: number;
}

export interface ForgejoRepository {
  owner: string;
  name: string;
  default_branch: string;
  bound_at: string;
}

// "ready" has a bound, cloneable repository and may create work orders.
// "needs_setup" means repository provisioning was interrupted or unavailable —
// retry via the repair endpoint. Absent on coordinators built before slice 1;
// treat absent as "ready" (an older coordinator always bound a repo on create).
export type RepositoryStatus = "ready" | "needs_setup";

export interface Project {
  id: string;
  name: string;
  recovery_policy: RecoveryPolicy;
  // Absent on coordinators built before these fields existed; guard on read.
  dialogue_limits?: DialogueLimits;
  agent_providers?: AgentProviders;
  agent_models?: AgentModels;
  merge_policy?: MergePolicy;
  autonomy_policy?: AutonomyPolicy;
  forgejo_repository?: ForgejoRepository;
  repository_status?: RepositoryStatus;
  created_at: string;
}

// POST /projects accepts these project defaults; each also has a PUT endpoint to
// change it later. agent_models requires agent_providers; omit models when they
// can't be resolved from the catalog so the backend applies its own defaults.
export interface CreateProjectInput {
  name: string;
  recovery_policy?: RecoveryPolicy;
  agent_providers?: AgentProviders;
  agent_models?: AgentModels;
  autonomy_policy?: AutonomyPolicy;
  merge_policy?: MergePolicy;
  dialogue_limits?: DialogueLimits;
}

// Runtime toolchain a project's agents get, provisioned via mise into the shared
// internal cache — never a file in the user's repository. Work-order creation is
// gated on status "configured" (backend returns 409 project_toolchain_required
// otherwise). `services` is planning metadata only: services_runnable is always
// false in this release — selecting a service does NOT start a server.
export const TOOL_NAMES = [
  "bun",
  "deno",
  "go",
  "java",
  "node",
  "php",
  "python",
  "ruby",
  "rust",
] as const;
export type ToolName = (typeof TOOL_NAMES)[number];
export type ToolchainStatus = "needs_setup" | "configured";
export type ToolchainSource = "picker" | "detected" | "assistant" | "runtime";

// Durable runtime install state for a configured toolchain. Saving sets it
// "pending"; the worker moves it to "installing" then "ready"/"failed". The
// first provider turn launches only after "ready".
export type ProvisioningStatus = "pending" | "installing" | "ready" | "failed";

export interface ProjectToolchain {
  project_id: string;
  status: ToolchainStatus;
  source?: ToolchainSource;
  tools: Record<string, string>; // tool name -> exact version
  services: string[];
  services_runnable: boolean;
  provisioning_status?: ProvisioningStatus;
  provisioning_message?: string;
  updated_at?: string;
}

export interface ToolchainPreset {
  id: string;
  display_name: string;
  description: string;
  tools: Record<string, string>;
  services?: string[];
}

export interface ToolchainSuggestion {
  tools: Record<string, string>;
  services: string[];
  evidence: string[];
  confidence: string;
}

export interface UpdateToolchainInput {
  source: ToolchainSource;
  tools: Record<string, string>;
  services?: string[];
}

// Guided ("help me choose") stack setup: a bounded provider conversation that
// ends in an exact proposal the user applies. It never touches the repository.
export type AssistantStatus =
  | "running"
  | "waiting_for_user"
  | "proposal_ready"
  | "applied"
  | "failed";

export interface AssistantMessage {
  role: string; // "user" | "assistant"
  text: string;
  occurred_at: string;
}

export interface AssistantSession {
  id: string;
  project_id: string;
  provider: AgentProvider;
  model: string;
  status: AssistantStatus;
  message?: string; // current question, proposal note, or failure reason
  proposal?: ToolchainSuggestion; // present when status is proposal_ready
  messages: AssistantMessage[];
  created_at: string;
  updated_at: string;
}

export interface StartAssistantInput {
  provider: AgentProvider;
  model: string;
  message: string;
}

export type FeatureState =
  | "draft"
  | "planning"
  | "implementing"
  | "reviewing"
  | "ready_to_merge"
  | "completed"
  | "cancelled";

export interface Feature {
  id: string;
  project_id: string;
  title: string;
  description: string;
  state: FeatureState;
  accepted_goal?: string;
  goal_accepted_at?: string;
  created_at: string;
  updated_at: string;
}

export interface CreateFeatureInput {
  title: string;
  description?: string;
  // Optional per-order overrides. Each omitted field falls back to the project
  // default; the run snapshots the effective values. (Backend handling lands
  // with the per-order-overrides slice; the coordinator ignores these until.)
  agent_providers?: AgentProviders;
  // Exact model IDs per role. Complete object when present. Backend validates
  // the resolved provider/model pair.
  agent_models?: AgentModels;
  autonomy_policy?: AutonomyPolicy;
  merge_policy?: MergePolicy;
  dialogue_limits?: DialogueLimits;
}

export type RunStatus =
  | "running"
  | "waiting_for_user"
  | "succeeded"
  | "stopped"
  | "failed";

// Machine-readable reason a run is waiting or paused. Empty when running.
// Clients pick controls/labels from this — never from parsing `reason` text.
export type WaitKind =
  | ""
  | "phase_checkpoint"
  | "round_cap"
  | "blocker"
  | "merge_gate"
  | "clarification"
  | "paused";

export interface Session {
  id: string;
  run_id: string;
  agent_id: string;
  role: string;
  status: string;
  outcome?: string;
  disposition?: string;
  summary?: string;
  recovery_attempt: number;
  started_at: string;
  updated_at: string;
  ended_at?: string;
}

// A user message addressed to one long-lived agent conversation.
export type InterventionTargetRole = "lead" | "reviewer";

export interface InterventionTarget {
  role: InterventionTargetRole;
  session_id: string;
}

export type InterventionStatus =
  | "waiting_for_boundary" // current bounded turn still finishing
  | "queued" // run paused + waiting, coordinator not yet admitted delivery
  | "being_answered" // agent is answering
  | "answered"; // visible answer + structured effect are durable

// The agent's explicit classification of what the message means, returned with
// an answered intervention (never inferred from prose).
export type InterventionEffect =
  | "guidance_applied" // honored without changing the accepted goal or plan
  | "clarification_required" // the agent needs another user exchange
  | "replanning_required"; // scope/plan changed → the order returns to planning

export interface Intervention {
  id: string;
  session_id: string;
  target: InterventionTargetRole;
  message: string;
  status: InterventionStatus;
  effect?: InterventionEffect;
  requested_at: string;
  updated_at: string;
  answered_at?: string;
  // Set once `guidance_applied` has been consumed by a successful /resume.
  resolved_at?: string;
}

export interface Run {
  id: string;
  feature_id: string;
  status: RunStatus;
  reason?: string;
  // Present on newer coordinators; guard on read. `wait_kind` is meaningful only
  // while waiting or paused.
  paused?: boolean;
  wait_kind?: WaitKind;
  autonomy_policy?: AutonomyPolicy;
  // Immutable per-run snapshots (effective provider/model per role).
  agent_providers?: AgentProviders;
  agent_models?: AgentModels;
  // Agreement cycle: 1 for the original plan; advances when a scope-changing
  // intervention is admitted into a revised plan.
  plan_version?: number;
  // Non-terminal lead/reviewer sessions a message may target (empty on terminal
  // runs). `intervention` describes the latest request once one has been made.
  intervention_targets?: InterventionTarget[];
  intervention?: Intervention;
  started_at: string;
  updated_at: string;
  ended_at?: string;
  sessions: Session[];
}

export type SessionEventType =
  | "user_message"
  | "message"
  | "plan_submitted"
  | "activity"
  | "input_required"
  | "pause_acknowledged"
  | "continued"
  | "recovery_assessment";

// Optional structured detail on an `activity` event. `text` stays the
// human-readable fallback; render this when present.
export type FileChangeOp = "created" | "modified" | "deleted" | "renamed";

export type ActivityDetail =
  | { kind: "narration" } // presentation-safe agent preamble / reasoning summary (text carries it)
  | { kind: "command"; command: string; exit_code?: number; duration_ms?: number }
  | {
      kind: "file_change";
      op: FileChangeOp;
      path: string;
      old_path?: string;
      additions?: number;
      deletions?: number;
    };

export interface SessionEvent {
  id: string;
  sequence: number;
  type: SessionEventType;
  text: string;
  activity?: ActivityDetail;
  occurred_at: string;
}

export interface Workspace {
  id: string;
  project_id: string;
  feature_id: string;
  base_branch: string;
  branch: string;
  base_commit_id: string;
  status: string;
  branch_created_at?: string;
  checkout?: { workspace_id: string; relative_path: string; created_at: string };
  pull_request?: { number: number; url: string; draft: boolean; recorded_at: string };
  merge?: {
    approved_commit_id: string;
    ready_at: string;
    merge_commit_id?: string;
    merged_at?: string;
  };
  created_at: string;
  updated_at: string;
}

// A durable workflow-history event. State-change events carry previous_state
// and state; they give the phase-boundary timestamps used to scope each phase's
// activity. Other event types (e.g. feature.goal_accepted) are ignored for that.
export interface WorkflowEvent {
  id: string;
  type: string; // "feature.state_changed" | "feature.goal_accepted" | ...
  sequence: number;
  occurred_at: string;
  previous_state?: FeatureState;
  state?: FeatureState;
}

/** One ordered lead/reviewer message in the planning discussion. */
export interface PlanningMessage {
  id: string;
  // Which agreement cycle authored this message; sequence stays monotonic across
  // versions so cursors don't restart when replanning begins.
  plan_version?: number;
  sequence: number;
  session_id: string;
  agent_id: string;
  role: string; // "lead" | "reviewer"
  type: SessionEventType; // "message" | "plan_submitted" | ...
  text: string;
  occurred_at: string;
}
