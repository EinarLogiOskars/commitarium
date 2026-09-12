// Domain types mirroring the coordinator contract (docs/coordinator-api.md).

export type RecoveryPolicy = "approval_required" | "automatic";

export type AgentProvider = "codex" | "claude";
export interface AgentProviders {
  lead: AgentProvider;
  reviewer: AgentProvider;
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

export interface Project {
  id: string;
  name: string;
  recovery_policy: RecoveryPolicy;
  // Absent on coordinators built before these fields existed; guard on read.
  dialogue_limits?: DialogueLimits;
  agent_providers?: AgentProviders;
  merge_policy?: MergePolicy;
  autonomy_policy?: AutonomyPolicy;
  forgejo_repository?: ForgejoRepository;
  created_at: string;
}

export interface CreateProjectInput {
  name: string;
  recovery_policy?: RecoveryPolicy;
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

export interface SessionEvent {
  id: string;
  sequence: number;
  type: SessionEventType;
  text: string;
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

/** One ordered lead/reviewer message in the planning discussion. */
export interface PlanningMessage {
  id: string;
  sequence: number;
  session_id: string;
  agent_id: string;
  role: string; // "lead" | "reviewer"
  type: SessionEventType; // "message" | "plan_submitted" | ...
  text: string;
  occurred_at: string;
}
