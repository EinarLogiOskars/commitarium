import { developerModeOn } from "../devMode";
import { request } from "./client";

// The assistant (ASSISTANT_PLAN.md): one conversation at the top level
// ("commitarium") and one per project, with actions the user approves.

export interface AssistantSettings {
  agent: string;
  model: string;
  /** The models that need explicit approval: the model guide's Premium and
   * unrated models (ADR-031). Read-only; tiers are set in the model guide. */
  ask_before: string[];
  /** The highest reasoning effort the assistant may choose. */
  max_effort?: string;
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
  | "continue_work_order"
  | "close_work_order"
  | "create_app"
  | "plan_project"
  | "set_up_preview"
  | "write_vision"
  | "set_up_stack"
  | "answer_helper"
  | "write_brief"
  | "accept_brief"
  | "design_stack"
  | "apply_stack"
  | "research";

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
  /** For continue_work_order: what to tell its agents; empty retries. For
   * set_up_preview: what fails, when the assistant knows. For answer_helper:
   * the assistant's answer. */
  guidance?: string;
  /** For answer_helper: the helper it answers; for a card from a helper's
   * result, the helper that proposed it (ADR-035). */
  helper?: string;
  /** For create_work_order: its brief is written before it starts. */
  brief?: boolean;
  /** For create_app and plan_project: all the assistant gathered, for the
   * app's planner, and what it assumed where the user did not say (ADR-035). */
  app_brief?: string;
  assumptions?: string[];
  /** For add_vision_item and start_autopilot: 0 for P0, 1 for P1, and so on. */
  priority: number;
  /** For a work order to create or start: the work order ID, or the title of
   * another action, it waits for; it starts once that has merged. */
  after?: string;
  status: "proposed" | "applied" | "dismissed" | "failed";
  premium?: string[];
  result?: string;
  /** The project a create_app or plan_project action created. */
  project_id?: string;
  /** Autopilot took it over. */
  auto_tried?: boolean;
  /** Autopilot holds it until the work it follows (after) has merged. */
  queued?: boolean;
  /** Autopilot created the work order without starting it; a queued start follows. */
  hold_start?: boolean;
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
  /** The user's messages sent while the assistant answered; they start its
   * next turn. */
  queued?: AssistantMessage[];
  /** A new app being set up, until it starts building (APPS_PLAN.md). */
  launch?: { stage: LaunchStage; started_at: string };
  updated_at: string;
  /** Set while the last turn waits for its agent's login: the message is
   * kept and sent once the agent works (needs_user: the user signs in). */
  sign_in?: { agent: string; since: string; needs_user?: boolean; retry_at?: string };
  /** The item the user opened the assistant on (ADR-035). */
  focus?: AssistantFocus;
}

/** What the conversation is about: a work order, the vision, or the stack. */
export interface AssistantFocus {
  kind: "work_order" | "vision" | "stack";
  work_order_id?: string;
  /** What the chip says; the coordinator fills it in. */
  label?: string;
}

export type LaunchStage = "vision" | "setup" | "building";

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
    body: { message, developer_mode: developerModeOn() },
    idempotencyKey: key,
  });

/** Focus the conversation on an item, or clear the focus with null. */
export const setAssistantFocus = (
  scope: string,
  focus: AssistantFocus | null,
): Promise<Conversation> =>
  focus
    ? request(`${conversationPath(scope)}/focus`, { method: "PUT", body: focus })
    : request(`${conversationPath(scope)}/focus`, { method: "DELETE" });

export const saveAssistantNotes = (scope: string, notes: string): Promise<Conversation> =>
  request(`${conversationPath(scope)}/notes`, { method: "PUT", body: { notes } });

/** Run an action the assistant proposed. A premium model needs
 * confirm_premium; start also starts a created work order. */
export const approveAssistantAction = (
  scope: string,
  actionId: string,
  options: {
    start?: boolean;
    confirm_premium?: boolean;
    /** The user's choice over the card's agents and models, for a new or
     * started work order. */
    lead_agent?: string;
    lead_model?: string;
    reviewer_agent?: string;
    reviewer_model?: string;
  },
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

/** The autopilot goals that are on, and the apps still being set up, by
 * project ID. */
export const listAutopilotGoals = (): Promise<{
  goals: Record<string, AutopilotGoal>;
  launches?: Record<string, LaunchStage>;
}> => request("/api/v1/assistant/autopilot");

/** Launch a new app: once its vision is written, the coordinator sets up
 * its stack and builds the whole vision on autopilot. */
export const startLaunch = (scope: string): Promise<Conversation> =>
  request(`${conversationPath(scope)}/launch`, { method: "POST" });
