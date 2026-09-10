// Domain types mirroring the coordinator contract (docs/coordinator-api.md).

export type RecoveryPolicy = "approval_required" | "automatic";

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
  // Absent on coordinators built before dialogue limits existed; guard on read.
  dialogue_limits?: DialogueLimits;
  forgejo_repository?: ForgejoRepository;
  created_at: string;
}

export interface CreateProjectInput {
  name: string;
  recovery_policy?: RecoveryPolicy;
}
