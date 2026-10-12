import { request } from "./client";

// The assistant (ASSISTANT_PLAN.md): one conversation at the top level
// ("commitarium") and one per project, with actions the user approves.

export interface AssistantSettings {
  agent: string;
  model: string;
  /** Model families that need explicit approval, e.g. "fable", "astra". */
  ask_before: string[];
  updated_at?: string;
}

export interface AssistantMessage {
  id: string;
  role: "user" | "assistant" | "event";
  text: string;
  occurred_at: string;
}

export type AssistantActionType =
  | "create_work_order"
  | "start_work_order"
  | "change_models"
  | "add_vision_item"
  | "start_autopilot"
  | "continue_work_order";

export interface AssistantAction {
  id: string;
  message_id: string;
  type: AssistantActionType;
  reason: string;
  title: string;
  description: string;
  vision_item_id: string;
  steps: "" | "full" | "planned" | "quick";
  independent_tests: boolean;
  lead_agent: string;
  lead_model: string;
  reviewer_agent: string;
  reviewer_model: string;
  work_order_id: string;
  when: "" | "now" | "next_turn";
  /** For continue_work_order: what to tell its agents; empty retries. */
  guidance?: string;
  /** For add_vision_item and start_autopilot: 0 for P0, 1 for P1, and so on. */
  priority: number;
  status: "proposed" | "applied" | "dismissed" | "failed";
  premium?: string[];
  result?: string;
  /** Autopilot took it over. */
  auto_tried?: boolean;
}

/** An autopilot goal: the open items of one vision priority. */
export interface AutopilotGoal {
  title: string;
  level: number;
  items: { id: string; label: string; title: string; work_order_id?: string; done: boolean }[];
  status: "active" | "done" | "stopped";
  started_at: string;
  ended_at?: string;
}

export interface Conversation {
  scope: string;
  status: "idle" | "running";
  agent: string;
  model: string;
  messages: AssistantMessage[];
  actions: AssistantAction[];
  notes: string;
  goal?: AutopilotGoal;
  updated_at: string;
}

const conversationPath = (scope: string) =>
  `/api/v1/assistant/conversations/${encodeURIComponent(scope)}`;

export const getAssistantSettings = (): Promise<AssistantSettings> =>
  request("/api/v1/assistant/settings");

export const saveAssistantSettings = (settings: AssistantSettings): Promise<AssistantSettings> =>
  request("/api/v1/assistant/settings", { method: "PUT", body: settings });

export const getConversation = (scope: string): Promise<Conversation> =>
  request(conversationPath(scope));

export const sendToAssistant = (
  scope: string,
  message: string,
  key: string,
): Promise<Conversation> =>
  request(`${conversationPath(scope)}/messages`, {
    method: "POST",
    body: { message },
    idempotencyKey: key,
  });

export const saveAssistantNotes = (scope: string, notes: string): Promise<Conversation> =>
  request(`${conversationPath(scope)}/notes`, { method: "PUT", body: { notes } });

/** Run an action the assistant proposed. A premium model needs
 * confirm_premium; start also starts a created work order. */
export const approveAssistantAction = (
  scope: string,
  actionId: string,
  options: { start?: boolean; confirm_premium?: boolean },
): Promise<Conversation> =>
  request(`${conversationPath(scope)}/actions/${encodeURIComponent(actionId)}/approve`, {
    method: "POST",
    body: options,
  });

export const dismissAssistantAction = (scope: string, actionId: string): Promise<Conversation> =>
  request(`${conversationPath(scope)}/actions/${encodeURIComponent(actionId)}/dismiss`, {
    method: "POST",
  });

export const startAutopilot = (scope: string, level: number): Promise<Conversation> =>
  request(`${conversationPath(scope)}/autopilot`, { method: "POST", body: { level } });

export const stopAutopilot = (scope: string): Promise<Conversation> =>
  request(`${conversationPath(scope)}/autopilot`, { method: "DELETE" });
