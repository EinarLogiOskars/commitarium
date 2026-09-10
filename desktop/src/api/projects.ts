import { request } from "./client";
import type { CreateProjectInput, Project } from "./types";

export const listProjects = (): Promise<Project[]> => request("/api/v1/projects");

export const getProject = (id: string): Promise<Project> =>
  request(`/api/v1/projects/${encodeURIComponent(id)}`);

export const createProject = (input: CreateProjectInput): Promise<Project> =>
  request("/api/v1/projects", { method: "POST", body: input });
