// Domain types mirroring the coordinator contract (docs/coordinator-api.md).

export type RecoveryPolicy = "approval_required" | "automatic";

export type AgentProvider = "codex" | "claude";
export interface AgentProviders {
  lead: AgentProvider;
  reviewer: AgentProvider;
}

export type MergePolicy = "require_user_approval" | "auto_after_gates";

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

export interface Run {
  id: string;
  feature_id: string;
  status: RunStatus;
  reason?: string;
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
