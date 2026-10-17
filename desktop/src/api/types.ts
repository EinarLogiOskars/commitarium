// Domain types mirroring the coordinator contract (docs/coordinator-api.md).

export type RecoveryPolicy = "approval_required" | "automatic";

export type RecoveryAction = "recheck" | "continue" | "replace" | "leave_paused";

export interface RecoveryStatus {
  action: RecoveryAction;
  role?: AgentRole;
  provider?: AgentProvider;
  classification:
    | "rechecking_durable_effects"
    | "confirmed_from_effects"
    | "incomplete_result"
    | "unresolved"
    | "left_paused";
  evidence: string[];
  missing_result?: string;
  process_status: "confirmed" | "successor_running" | "paused";
  successor: "none" | "resume_then_fresh" | "fresh_conversation";
  available_actions: RecoveryAction[];
}

export type AgentProvider = "codex" | "claude";
/** Lead and reviewer providers and agents (ADR-016). The provider follows
 * from the agent; without agents, the providers name the agents migrated
 * from the provider profiles. Responses always carry all four. */
export interface AgentProviders {
  lead: AgentProvider;
  reviewer: AgentProvider;
  lead_agent?: string;
  reviewer_agent?: string;
}

/** Exact model IDs per role (never floating aliases like "latest"). */
export interface AgentModels {
  lead: string;
  reviewer: string;
}

/** A work order's optional choices by phase (ADR-031): the model and effort
 * each role plans with, and its effort after the plan. Empty keeps the role's
 * model and the model's default effort. */
export interface PhaseChoices {
  lead_plan_model?: string;
  lead_plan_effort?: string;
  lead_effort?: string;
  reviewer_plan_model?: string;
  reviewer_plan_effort?: string;
  reviewer_effort?: string;
}

export type AgentRole = "lead" | "reviewer";

/** One selectable model from a provider+role catalog. */
export interface ModelInfo {
  id: string;
  display_name: string;
  default_reasoning_effort?: string;
  supported_reasoning_efforts?: string[];
}

/** One provider account — a subscription or an API key — that can lead or
 * review any number of work orders (ADR-016). */
export interface Agent {
  id: string;
  name: string;
  provider: AgentProvider;
  created_at: string;
  updated_at: string;
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

/** How the assistant works in a project (ADR-027): it assists by default,
 * proposing cards the user approves and leaving the user's work orders to
 * them; or it builds everything (on for a project the top-level assistant
 * created), owning the whole project, the user's work orders included. */
export type AssistantMode = "builds" | "assists";

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
  /** Deprecated (ADR-027): a project no longer chooses agents or models; new
   * projects have none and older ones' are not used for new work. */
  agent_providers?: AgentProviders;
  agent_models?: AgentModels;
  merge_policy?: MergePolicy;
  autonomy_policy?: AutonomyPolicy;
  independent_acceptance_tests?: boolean; // project default for future work orders
  /** The user uses it as an app (APPS_PLAN.md). */
  app?: boolean;
  /** Absent on older coordinators: read it through assistantMode(). */
  assistant_mode?: AssistantMode;
  forgejo_repository?: ForgejoRepository;
  repository_status?: RepositoryStatus;
  created_at: string;
}

export interface ProjectDeletionResult {
  project_id: string;
  deleted: boolean;
}

// POST /projects accepts these project defaults; each also has a PUT endpoint to
// change it later. Agents and models are not project defaults (ADR-027).
export interface CreateProjectInput {
  name: string;
  recovery_policy?: RecoveryPolicy;
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
  "gradle",
  "java",
  "maven",
  "node",
  "php",
  "pnpm",
  "python",
  "ruby",
  "rust",
  "uv",
] as const;
export type ToolName = (typeof TOOL_NAMES)[number];
export type ToolchainStatus = "needs_setup" | "configured";
export type ToolchainSource = "picker" | "detected" | "assistant" | "runtime";

// Durable runtime install state for a configured toolchain. Saving sets it
// "pending"; the worker moves it to "installing" then "ready"/"failed". The
// first provider turn launches only after "ready".
export type ProvisioningStatus = "pending" | "installing" | "ready" | "failed";

// What a preview opens in the browser (ADR-014). The preview runs the compose
// file at the repository root; this names the service and container port.
export interface RunConfig {
  open: { service: string; port: number };
}

export interface ProjectToolchain {
  project_id: string;
  status: ToolchainStatus;
  source?: ToolchainSource;
  tools: Record<string, string>; // tool name -> exact version
  services: string[];
  services_runnable: boolean;
  run?: RunConfig;
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
  run?: RunConfig;
  evidence: string[];
  confidence: string;
}

export interface UpdateToolchainInput {
  source: ToolchainSource;
  tools: Record<string, string>;
  services?: string[];
  run?: RunConfig; // replacement semantics: omitting it removes it
}

// Guided ("help me choose") stack setup: a bounded provider conversation that
// ends in an exact proposal the user applies. It never touches the repository.
export type AssistantStatus =
  "running" | "waiting_for_user" | "proposal_ready" | "applied" | "failed";

export interface AssistantMessage {
  role: string; // "user" | "assistant"
  text: string;
  occurred_at: string;
}

// design_stack: describe a new project and get a proposal. verify_repository:
// the agent inspects an imported repo's committed head and proposes/explains a
// stack (no user description needed).
export type AssistantPurpose = "design_stack" | "verify_repository";

export interface AssistantSession {
  id: string;
  project_id: string;
  agent: string;
  model: string;
  purpose?: AssistantPurpose;
  status: AssistantStatus;
  message?: string; // current question, proposal note, or failure reason
  proposal?: ToolchainSuggestion; // present when status is proposal_ready
  verified_commit_id?: string; // set for verify_repository responses
  messages: AssistantMessage[];
  created_at: string;
  updated_at: string;
}

export interface StartAssistantInput {
  agent?: string;
  provider?: AgentProvider; // names its migrated agent when agent is omitted
  model: string;
  purpose?: AssistantPurpose; // omitted preserves design_stack
  message?: string; // required for design_stack; omitted for verify_repository
}

export type FeatureState =
  | "draft"
  | "ready"
  | "planning"
  | "implementing"
  | "reviewing"
  | "ready_to_merge"
  | "completed"
  | "cancelled";

/** A work order's optional steps (ADR-018). A reviewer requires a documented
 * plan, so the valid choices are Full, Planned, and Quick change. */
export interface WorkSteps {
  documented_plan: boolean;
  reviewer: boolean;
  /** False keeps the reviewer for the plan only ("Reviewed plan", ADR-031);
   * absent on older coordinators, where a reviewer always reviews code. */
  code_review?: boolean;
}

export interface Feature {
  id: string;
  project_id: string;
  title: string;
  description: string;
  state: FeatureState;
  accepted_goal?: string;
  goal_accepted_at?: string;
  independent_acceptance_tests?: boolean;
  // The settings it starts with. Agents and models are empty until it is
  // created or started with a choice (ADR-027); the run snapshots them.
  agent_providers?: AgentProviders;
  agent_models?: AgentModels;
  /** On a single work order: its choices by phase, if any. */
  phase_choices?: PhaseChoices;
  autonomy_policy?: AutonomyPolicy;
  merge_policy?: MergePolicy;
  steps?: WorkSteps;
  // "session" is an interactive session with one agent (ADR-019); absent on
  // older coordinators, which only have work orders.
  kind?: WorkKind;
  // A work order Commitarium runs by itself, such as the preview setup; not
  // listed with the user's work.
  background?: boolean;
  // "user" for a work order made by hand, which the assistant never acts on
  // by itself; "assistant" for its own (ADR-027). Absent on older coordinators.
  created_by?: "user" | "assistant";
  // Merges only once Commitarium's preview check of its commit passed: the
  // preview setup and fixes, and a project's setup (ADR-028).
  preview_check?: boolean;
  // The work this one waits for before the assistant starts it, so two
  // changes to the same part of the app don't run at once. The user may
  // still start it, after a warning.
  queued_after?: string;
  // A session's agent activity, in feature lists only: working on a message,
  // or idle since `updated_at`.
  session?: { working: boolean; updated_at: string };
  created_at: string;
  updated_at: string;
}

export type WorkKind = "work_order" | "session";

export interface CreateFeatureInput {
  title: string;
  // "session" creates and starts a session (ADR-019); its title may be empty.
  kind?: WorkKind;
  description?: string;
  // Optional per-order settings. Agents and models are the user's choice
  // (ADR-027); omitted, they are chosen at Start. agent_models needs
  // agent_providers; the coordinator checks each model against its agent.
  agent_providers?: AgentProviders;
  agent_models?: AgentModels;
  autonomy_policy?: AutonomyPolicy;
  merge_policy?: MergePolicy;
  dialogue_limits?: DialogueLimits;
  // Per-order override; omitted inherits the project default.
  independent_acceptance_tests?: boolean;
}

export type RunStatus = "running" | "waiting_for_user" | "succeeded" | "stopped" | "failed";

// Machine-readable reason a run is waiting or paused. Empty when running.
// Clients pick controls/labels from this — never from parsing `reason` text.
export type WaitKind =
  "" | "phase_checkpoint" | "round_cap" | "blocker" | "merge_gate" | "clarification" | "paused";

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

/** What blocks a run (ADR-032). */
export interface RunBlocker {
  kind: "agent" | "check" | "environment" | "limit" | "other";
  check?: string;
  detail: string;
  fingerprint: string;
  recorded_at: string;
}

/** A recovery tried on a blocked run. */
export interface RunRecoveryAttempt {
  how: "recheck" | "continue" | "message" | "fresh_conversation" | "back_to_ready";
  guidance?: string;
  from_assistant: boolean;
  fingerprint: string;
  created_at: string;
}

export interface Run {
  id: string;
  feature_id: string;
  status: RunStatus;
  reason?: string;
  /** On a run waiting on a blocker; absent on older coordinators. */
  blocker?: RunBlocker;
  recoveries?: RunRecoveryAttempt[];
  /** Recovering the blocker ran out: report it or start over. */
  recovery_exhausted?: boolean;
  // Present on newer coordinators; guard on read. `wait_kind` is meaningful only
  // while waiting or paused.
  paused?: boolean;
  wait_kind?: WaitKind;
  autonomy_policy?: AutonomyPolicy;
  // Immutable per-run snapshots (effective provider/model per role).
  agent_providers?: AgentProviders;
  agent_models?: AgentModels;
  // The optional steps the run started with; absent on older coordinators.
  steps?: WorkSteps;
  // Agreement cycle: 1 for the original plan; advances when a scope-changing
  // intervention is admitted into a revised plan.
  plan_version?: number;
  // Non-terminal lead/reviewer sessions a message may target (empty on terminal
  // runs). `intervention` describes the latest request once one has been made.
  intervention_targets?: InterventionTarget[];
  // Set while the run waits for an agent's provider login to be renewed.
  login_required?: { agent: string; role: "lead" | "reviewer" };
  // Set while the run waits for an agent's usage limit to reset; it continues
  // by itself at resets_at (or every 15 minutes when unknown).
  usage_limit?: { agent: string; role: "lead" | "reviewer"; resets_at?: string };
  // Set while an interrupted turn (a worker restart, a lost connection) waits
  // for its automatic retry, retry of `of`, due at due_at.
  retrying?: { cause: string; retry: number; of: number; due_at: string };
  intervention?: Intervention;
  // Present on the immediate response to an explicit recovery action. Normal
  // run reads remain unchanged.
  recovery?: RecoveryStatus;
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
  // Present on a final agent message that was previewed live; ties it to the
  // transient message_preview frames so the UI can replace the preview.
  stream_id?: string;
}

// A transient, non-durable live preview of an agent's final response prose.
// Cumulative (full visible prefix so far, not an append), never persisted or
// replayed. Delivered over the session SSE as `event: message_preview` with no
// SSE id; the durable message with the same stream_id supersedes it.
export interface MessagePreview {
  stream_id: string;
  text: string;
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
  type: string; // "feature.state_changed" | "feature.goal_accepted" | "feature.artifact_updated" | ...
  sequence: number;
  occurred_at: string;
  previous_state?: FeatureState;
  state?: FeatureState;
  // Present on "feature.artifact_updated": which durable artifact changed and to
  // what revision — the UI refetches that artifact when it sees a higher one.
  artifact_kind?: FeatureArtifactKind;
  artifact_revision?: number;
}

// Durable, revisioned feature documents kept in coordinator SQLite (never Git):
// the proposed goal and the agreed implementation plan. They are separate from
// the conversational session transcript.
export interface TokenUsage {
  /** Excludes cached reads and cache writes. */
  input_tokens: number;
  cached_input_tokens: number;
  cache_write_tokens: number;
  output_tokens: number;
}

export interface RoleUsage extends TokenUsage {
  role: string;
}

export interface PhaseUsage extends TokenUsage {
  /** clarify, plan, plan_approval, implement, acceptance_tests, review,
   * correction, readiness, intervention, or other. */
  phase: string;
  role: string;
  /** The models and efforts that spent it; absent on older coordinators. */
  models?: ModelUsage[];
}

export interface ModelUsage extends TokenUsage {
  model: string;
  /** Empty or absent: the model's default effort. */
  effort?: string;
  /** What its API-key turns cost; absent when none was priced (ADR-031). */
  cost_usd?: number;
}

/** What usage cost (ADR-031): API-key turns in dollars; the rest in tokens. */
export interface CostSummary {
  api_cost_usd: number;
  /** API-key turns on a model without a price. */
  api_unpriced: TokenUsage;
  subscription: TokenUsage;
  /** Turns whose account did not say how it pays. */
  unknown: TokenUsage;
  prices_as_of?: string;
}

/** Provider-reported token totals for a work order, by role and by phase. */
export interface FeatureUsage {
  roles: RoleUsage[];
  /** Absent on older coordinators. */
  phases?: PhaseUsage[];
  total: TokenUsage;
  /** Absent on older coordinators. */
  cost?: CostSummary;
}

export interface UsageEntry extends TokenUsage {
  cost_usd?: number;
}

/** A project's usage across its work orders and its assistant. */
export interface ProjectUsage {
  total: TokenUsage;
  cost: CostSummary;
  /** Ordered by cost, then input tokens. */
  models: (UsageEntry & { model: string })[];
  work_orders: (UsageEntry & { id: string; title: string })[];
  /** The assistant's own conversation turns in the project. */
  assistant: UsageEntry;
}

export type FeatureArtifactKind =
  "goal_draft" | "implementation_plan" | "acceptance_tests" | "handoff_brief";

/** A clarified work order: what and why, where, and what to plan around. */
export interface HandoffBriefDocument {
  goal: string;
  areas: string[];
  considerations: string[];
  open_questions: string[];
  /** The default-branch commit the brief was written against. */
  base_commit_id: string;
}

export type WorkOrderAssistantStatus = "running" | "waiting_for_user" | "proposal_ready" | "failed";

export interface WorkOrderAssistantMessage {
  role: "user" | "assistant";
  text: string;
  occurred_at: string;
}

/** A draft work order's clarification conversation with the project assistant. */
export interface WorkOrderAssistantSession {
  id: string;
  feature_id: string;
  agent: string;
  model: string;
  status: WorkOrderAssistantStatus;
  message: string;
  messages: WorkOrderAssistantMessage[];
  created_at: string;
  updated_at: string;
}

// The reviewer's private, blind acceptance-test checklist (titles + commit ids
// only — never executable source). pending = not yet run (unmet, not a failed
// execution); failed/not_applicable carry a human-readable note.
export type AcceptanceTestStatus = "pending" | "running" | "passed" | "failed" | "not_applicable";

export interface AcceptanceTest {
  id: string;
  position: number;
  title: string;
  status: AcceptanceTestStatus;
  note: string;
  // The command that runs only this test.
  command?: string;
  // Commitarium's own run of the test, by its exit code.
  coordinator_status?: "passed" | "failed";
}

export interface AcceptanceTestsDocument {
  plan_version: number;
  test_commit_id: string;
  implementation_commit_id: string; // pinned at review handoff; "" while authoring
  tests: AcceptanceTest[];
  // The commit Commitarium last ran the tests against itself.
  coordinator_commit_id?: string;
  // Why Commitarium's own run could not run the tests (dependencies would
  // not install, or the environment lacks one); its tests then have no
  // coordinator_status.
  coordinator_error?: string;
}

export interface GoalDraftDocument {
  goal: string;
  open_questions: string[];
}

export type PlanStepStatus = "pending" | "in_progress" | "completed";

export interface PlanStep {
  id: string;
  position: number;
  title: string;
  subtitle?: string;
  details_markdown?: string;
  verification?: string[];
  commit_subject?: string;
  status: PlanStepStatus;
  commit_id?: string;
  completed_at?: string;
}

export interface ImplementationPlanDocument {
  plan_version: number;
  title: string;
  subtitle?: string;
  steps: PlanStep[];
}

export interface FeatureArtifact<T = unknown> {
  feature_id: string;
  kind: FeatureArtifactKind;
  revision: number;
  document: T;
  updated_by?: { kind: string; id: string };
  updated_at: string;
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

// --- Phase 4: isolated validation + approved system packages ---

export type ValidationJobStatus = "pending" | "running" | "passed" | "failed";

export interface ValidationCommandResult {
  command: string;
  exit_code: number;
  output: string;
  duration_ms: number;
}

export interface ValidationJob {
  id: string;
  project_id: string;
  feature_id: string;
  run_id: string;
  workspace_id: string;
  commit_id: string;
  commands: string[];
  status: ValidationJobStatus;
  results: ValidationCommandResult[];
  error?: string;
  created_at: string;
  updated_at: string;
  completed_at?: string;
}

// Returned by the native run_validation_job command (Tauri claims + runs it).
// --- Preview checks (ADR-028) ---

/** What one compose service did: from a preview that failed, or from
 * Commitarium's preview check. */
export interface ServiceEvidence {
  service: string;
  state: string;
  exit_code: number;
  health?: string;
  logs?: string;
}

export interface PreviewCheckEvidence {
  stage: "source" | "setup" | "build" | "start" | "ready";
  reason?: string;
  /** "service:port" the check opened. */
  open?: string;
  /** The end of the build and start output. */
  output?: string;
  services: ServiceEvidence[];
}

export interface PreviewCheck {
  id: string;
  commit_id: string;
  attempt: number;
  status: "pending" | "running" | "passed" | "failed" | "expired";
  evidence?: PreviewCheckEvidence;
  created_at: string;
  completed_at?: string;
}

export interface ValidationRunResult {
  job: ValidationJob;
}

export type EnvironmentRequestStatus =
  "requested" | "approved" | "provisioning" | "ready" | "rejected" | "failed";

export interface EnvironmentRequest {
  id: string;
  project_id: string;
  feature_id: string;
  run_id: string;
  session_id: string;
  attempt_id: string;
  system_packages: string[];
  reason: string;
  status: EnvironmentRequestStatus;
  resolved_packages: Record<string, string>;
  error?: string;
  requested_at: string;
  updated_at: string;
  completed_at?: string;
}

// Returned by the native provision_environment_request command.
export interface EnvironmentProvisionResult {
  request: EnvironmentRequest;
  resolved_packages: Record<string, string>;
  codex_image: string;
  claude_image: string;
}

// --- Phase 5: global review inbox ---

export type AttentionKind =
  | "clarification"
  | "phase_checkpoint"
  | "round_cap"
  | "blocker"
  | "paused"
  | "merge_approval"
  | "environment_approval"
  | "environment_provisioning"
  | "environment_failed"
  | "validation_pending"
  | "validation_running"
  | "validation_failed"
  | "run_failed"
  | "auto_merge_completed"
  // An agent reached its usage limit; the run continues by itself at the reset.
  | "usage_limit"
  // An interrupted turn retries by itself; it becomes a `blocker` if the
  // retries run out.
  | "retrying"
  // A blocked or failed run autopilot is still continuing; it becomes a
  // `blocker` / `run_failed` item (a new ID) if autopilot gives up.
  | "autopilot_handling"
  // Autopilot has handled a blocked or failed run for a while without
  // finishing it: the user checks in with the assistant (a new ID, so it
  // notifies once).
  | "autopilot_stalled";

export interface AttentionItem {
  id: string;
  kind: AttentionKind;
  severity: "info" | "warning" | "error";
  actionable: boolean;
  project_id: string;
  project_name: string;
  feature_id: string;
  feature_title: string;
  run_id?: string;
  environment_request_id?: string;
  validation_job_id?: string;
  // A pending validation job the desktop runs by itself (auto-merge runs).
  auto_run?: boolean;
  title: string;
  detail: string;
  updated_at: string;
}

export interface AttentionResponse {
  generated_at: string;
  items: AttentionItem[];
  running: RunningWorkOrder[];
}

export interface RunningWorkOrder {
  project_id: string;
  project_name: string;
  feature_id: string;
  feature_title: string;
  run_id: string;
  updated_at: string;
}

// A project's vision (ADR-020): why it exists, and the prioritized plan that
// gets it there. Items nest up to three levels; only items without sub-items
// become work orders. Numbers (P0.1.2) come from position; IDs stay stable.
export interface VisionItem {
  id: string;
  title: string;
  description: string;
  items?: VisionItem[];
  feature_id?: string;
  // Already delivered without a work order (e.g. built before the vision).
  done?: boolean;
}

export interface VisionPriority {
  level: number;
  items: VisionItem[];
}

export type VisionConfidence = "high" | "medium" | "low";

export interface VisionPlan {
  statement: string;
  priorities: VisionPriority[];
  confidence: VisionConfidence;
  guesses: string[];
  // The recommended stack for a project without code yet.
  stack?: VisionStack;
}

export interface VisionStack {
  presets: string[];
  frameworks: string;
  reason: string;
}

export interface Vision {
  project_id: string;
  plan: VisionPlan;
  source: "assistant" | "user" | "repository";
  revision: number;
  updated_at: string;
}

export type VisionMode = "describe" | "converse";

export interface VisionSession {
  id: string;
  project_id: string;
  agent: string;
  model: string;
  mode: VisionMode;
  status: "running" | "waiting_for_user" | "written" | "failed";
  messages: { role: "user" | "assistant"; text: string; occurred_at: string }[];
  created_at: string;
  updated_at: string;
}
