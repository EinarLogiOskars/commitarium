import { request } from "./client";
import type {
  AgentProvider,
  AutonomyPolicy,
  CreateProjectInput,
  MergePolicy,
  Project,
} from "./types";

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
  default_branch: string;
  head: RepoOverviewHead;
  readme_markdown?: string;
  tree: RepoTreeEntry[];
}
export const getRepositoryOverview = (id: string): Promise<RepositoryOverview> =>
  request(`/api/v1/projects/${encodeURIComponent(id)}/repository-overview`);

export const getProject = (id: string): Promise<Project> =>
  request(`/api/v1/projects/${encodeURIComponent(id)}`);

export const createProject = (input: CreateProjectInput): Promise<Project> =>
  request("/api/v1/projects", { method: "POST", body: input });

const projectPath = (id: string) => `/api/v1/projects/${encodeURIComponent(id)}`;

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
