import { request } from "./client";
import type {
  AgentProvider,
  CreateProjectInput,
  MergePolicy,
  Project,
} from "./types";

export const listProjects = (): Promise<Project[]> => request("/api/v1/projects");

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
