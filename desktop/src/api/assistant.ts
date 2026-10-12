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
  "create_work_order" | "start_work_order" | "change_models" | "add_vision_item";

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
  /** For add_vision_item: 0 for P0, 1 for P1, and so on. */
  priority: number;
  status: "proposed" | "applied" | "dismissed" | "failed";
  premium?: string[];
  result?: string;
}

export interface Conversation {
  scope: string;
  status: "idle" | "running";
  agent: string;
  model: string;
  messages: AssistantMessage[];
  actions: AssistantAction[];
  notes: string;
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
