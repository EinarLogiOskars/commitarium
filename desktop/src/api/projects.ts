import { request, ApiError } from "./client";
import type {
  AgentModels,
  AgentProviders,
  AgentProvider,
  AutonomyPolicy,
  CreateProjectInput,
  MergePolicy,
  ModelsResponse,
  Project,
} from "./types";

// Model catalogs per provider+role (last-successful, refreshed on the backend).
export const getModels = (): Promise<ModelsResponse> => request("/api/v1/models");

/** User-invoked immediate refresh of all worker catalogs. */
export const refreshModels = (): Promise<ModelsResponse> =>
  request("/api/v1/models/refresh", { method: "POST" });

export const listProjects = (): Promise<Project[]> => request("/api/v1/projects");

// Read-only view of the internal repository (default-branch head, root tree,
// optional capped README). Throws ApiError with code repository_unavailable |
// content_too_large | project_not_found on failure.
export interface RepoOverviewHead {
  commit_id: string;
  message: string;
  author: string;
  committed_at: string;
}
export interface RepoTreeEntry {
  path: string;
  type: "file" | "dir";
}
export interface RepositoryOverview {
  url?: string; // browser-facing Forgejo URL at the default branch; omitted if unresolvable
  default_branch: string;
  head: RepoOverviewHead;
  readme_markdown?: string;
  tree: RepoTreeEntry[];
}
export const getRepositoryOverview = (id: string): Promise<RepositoryOverview> =>
  request(`/api/v1/projects/${encodeURIComponent(id)}/repository-overview`);

export const getProject = (id: string): Promise<Project> =>
  request(`/api/v1/projects/${encodeURIComponent(id)}`);

// Creation prepares a private Forgejo repository before returning. Always send a
// stable Idempotency-Key: a retry after an interruption resumes the *same*
// project + repository. On transient Forgejo failure the coordinator returns
// 503 repository_provisioning_unavailable, but the durable project still exists
// (visible in listProjects) with repository_status "needs_setup" — repair it
// with repairRepository rather than re-creating.
export const createProject = (
  input: CreateProjectInput,
  idempotencyKey: string,
): Promise<Project> => request("/api/v1/projects", { method: "POST", body: input, idempotencyKey });

// Create-or-repair the project's internal repository. Empty body, fresh key each
// call, safe to retry, no-op once the repo is ready. Never starts an agent.
export const repairRepository = (id: string): Promise<Project> =>
  request(`${projectPath(id)}/forgejo-repository`, {
    method: "POST",
    idempotencyKey: crypto.randomUUID(),
  });

const projectPath = (id: string) => `/api/v1/projects/${encodeURIComponent(id)}`;

// Project validation: an ordered, non-empty list of shell commands run in a
// disposable worker for the exact approved commit before the merge gate opens.
export interface ValidationConfig {
  commands: string[];
}

// GET returns 404 when validation was never configured — treat that as empty.
export const getProjectValidation = async (id: string): Promise<ValidationConfig> => {
  try {
    return await request<ValidationConfig>(`${projectPath(id)}/validation`);
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) return { commands: [] };
    throw e;
  }
};

// PUT stores the ordered command list. It must be non-empty — there is no
// disable/delete operation.
export const updateProjectValidation = (id: string, commands: string[]): Promise<ValidationConfig> =>
  request(`${projectPath(id)}/validation`, { method: "PUT", body: { commands } });

export const updateDialogueLimits = (
  id: string,
  planning_rounds: number,
  implementation_review_rounds: number,
): Promise<Project> =>
  request(`${projectPath(id)}/dialogue-limits`, {
    method: "PUT",
    body: { planning_rounds, implementation_review_rounds },
  });

export const updateAgentProviders = (
  id: string,
  lead: AgentProvider,
  reviewer: AgentProvider,
): Promise<Project> =>
  request(`${projectPath(id)}/agent-providers`, {
    method: "PUT",
    body: { lead, reviewer },
  });

/** Atomically set default providers AND exact models for future work orders.
 * The combined route validates each provider/model pair. */
export const updateAgentSettings = (
  id: string,
  agent_providers: AgentProviders,
  agent_models: AgentModels,
): Promise<Project> =>
  request(`${projectPath(id)}/agent-settings`, {
    method: "PUT",
    body: { agent_providers, agent_models },
  });

export const updateMergePolicy = (id: string, merge_policy: MergePolicy): Promise<Project> =>
  request(`${projectPath(id)}/merge-policy`, {
    method: "PUT",
    body: { merge_policy },
  });

export const updateAutonomyPolicy = (
  id: string,
  autonomy_policy: AutonomyPolicy,
): Promise<Project> =>
  request(`${projectPath(id)}/autonomy-policy`, {
    method: "PUT",
    body: { autonomy_policy },
  });
