import { request } from "./client";
import type {
  ProjectToolchain,
  ToolchainPreset,
  ToolchainSuggestion,
  UpdateToolchainInput,
} from "./types";

const projectPath = (id: string) => `/api/v1/projects/${encodeURIComponent(id)}`;

/** Curated stacks with explicit runtime versions (Python, Node LTS, Go, Rust). */
export const getToolchainPresets = (): Promise<{ presets: ToolchainPreset[] }> =>
  request("/api/v1/toolchain-presets");

/** The project's effective runtime toolchain. status "needs_setup" until saved. */
export const getProjectToolchain = (id: string): Promise<ProjectToolchain> =>
  request(`${projectPath(id)}/toolchain`);

/** Replace the toolchain with an exact set of tools (idempotent). At least one
 * tool is required; latest/system versions are rejected (400 invalid_toolchain). */
export const updateProjectToolchain = (
  id: string,
  input: UpdateToolchainInput,
): Promise<ProjectToolchain> =>
  request(`${projectPath(id)}/toolchain`, { method: "PUT", body: input });

/** Shallow, non-mutating suggestion from the repository root (for imports). */
export const detectProjectToolchain = (id: string): Promise<ToolchainSuggestion> =>
  request(`${projectPath(id)}/toolchain/detect`, { method: "POST" });
