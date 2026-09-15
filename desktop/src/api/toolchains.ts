import { request } from "./client";
import type {
  AssistantSession,
  ProjectToolchain,
  StartAssistantInput,
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

const assistantPath = (id: string) => `${projectPath(id)}/toolchain/assistant-sessions`;

/** Start a guided stack-setup conversation on a chosen lead provider + model. */
export const startAssistantSession = (
  id: string,
  input: StartAssistantInput,
  idempotencyKey: string,
): Promise<AssistantSession> =>
  request(assistantPath(id), { method: "POST", body: input, idempotencyKey });

/** Poll a stack-setup conversation. */
export const getAssistantSession = (id: string, sessionId: string): Promise<AssistantSession> =>
  request(`${assistantPath(id)}/${encodeURIComponent(sessionId)}`);

/** Answer the assistant's question. */
export const sendAssistantMessage = (
  id: string,
  sessionId: string,
  message: string,
  idempotencyKey: string,
): Promise<AssistantSession> =>
  request(`${assistantPath(id)}/${encodeURIComponent(sessionId)}/messages`, {
    method: "POST",
    body: { message },
    idempotencyKey,
  });

/** Apply a proposal_ready session as the project toolchain (source assistant). */
export const applyAssistantSession = (
  id: string,
  sessionId: string,
  idempotencyKey: string,
): Promise<ProjectToolchain> =>
  request(`${assistantPath(id)}/${encodeURIComponent(sessionId)}/apply`, {
    method: "POST",
    idempotencyKey,
  });
