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
  /** The files attached to it, among the scope's documents. */
  attachments?: string[];
  /** The assistant's questions with suggested answers (ADR-039), each
   * answered once. */
  questions?: AssistantQuestion[];
  occurred_at: string;
}

/** A question the assistant asked, with the answers it suggests. */
export interface AssistantQuestion {
  id: string;
  text: string;
  options: string[];
  /** The option the assistant would pick. */
  suggested: number;
  /** Set once answered; option is absent when the user wrote their own. */
  answer?: { option?: number; text: string; answered_at: string };
}

export type AssistantActionType =
  | "create_work_order"
  | "start_work_order"
  | "change_models"
  | "add_vision_item"
  | "start_autopilot"
  | "continue_work_order"
  | "recover_work_order"
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
  | "research"
  | "update_plan"
  | "build_plan"
  | "start_planning";

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
  /** For recover_work_order: how it recovers the blocked run. */
  how?: "" | "recheck" | "continue" | "message" | "fresh_conversation" | "back_to_ready";
  /** For answer_helper: the helper it answers; for a card from a helper's
   * result, the helper that proposed it (ADR-035). */
  helper?: string;
  /** For create_work_order: its brief is written before it starts. */
  brief?: boolean;
  /** For create_app and plan_project: all the assistant gathered, for the
   * app's planner, and what it assumed where the user did not say (ADR-035). */
  app_brief?: string;
  assumptions?: string[];
  /** For update_plan: the plan's sections (description is its goal). */
  decisions?: string[];
  open_questions?: string[];
  plan_items?: {
    priority: number;
    title: string;
    description: string;
    items?: { title: string; description: string }[];
  }[];
  /** For update_plan: the written plan, when this update drafts or
   * revises it. */
  plan_document?: string;
  stack_presets?: string[];
  frameworks?: string;
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
  /** Why what follows an applied create_app or plan_project failed: its
   * chat moving in, the app's start, or the project's planning. Approving
   * the card again finishes it on the same project. */
  start_failed?: string;
  /** Autopilot took it over. */
  auto_tried?: boolean;
  /** Autopilot left it for the user instead of applying it. */
  left_for_user?: boolean;
  /** Its work order already got where it leads, so it was not needed: it
   * counts as applied, and result says why ("Already started."). */
  settled?: boolean;
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
  /** The chat this is (ADR-038), and its title; absent on older coordinators. */
  chat?: string;
  title?: string;
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
  /** Research still being looked up; the user can carry on meanwhile. */
  researching?: string[];
  updated_at: string;
  /** Set while the last turn waits for its agent's login: the message is
   * kept and sent once the agent works (needs_user: the user signs in). */
  sign_in?: { agent: string; since: string; needs_user?: boolean; retry_at?: string };
  /** The item the user opened the assistant on (ADR-035). */
  focus?: AssistantFocus;
  /** When the current or last turn started, its automatic retries included. */
  turn_started_at?: string;
  /** What the assistant is doing while it answers. */
  activity?: AssistantActivity;
  /** The last turn ended without a reply once its automatic retries ran out. */
  failure?: { message_id: string; detail?: string };
}

/** The assistant's latest step while it answers: plain words, and what it
 * looked at (a path, a command, a search) for Developer mode. */
export interface AssistantActivity {
  label: string;
  detail?: string;
  started_at?: string;
  /** The automatic retry running, from 1. */
  retry?: number;
}

/** What the conversation is about: a work order, the vision, or the stack. */
export interface AssistantFocus {
  kind: "work_order" | "vision" | "stack";
  work_order_id?: string;
  /** What the chip says; the coordinator fills it in. */
  label?: string;
}

export type LaunchStage = "vision" | "setup" | "building";

/** A chat in a scope (ADR-038). */
export interface ChatSummary {
  id: string;
  scope: string;
  title: string;
  status: "idle" | "running";
  archived?: boolean;
  /** When the user last wrote in it. */
  used_at: string;
  created_at: string;
  updated_at: string;
}

/** A scope, which acts on its most recently used chat, or one chat in it. */
export type ChatTarget = string | { scope: string; chat?: string };

const conversationPath = (target: ChatTarget) => {
  const { scope, chat } = typeof target === "string" ? { scope: target, chat: undefined } : target;
  const path = `/api/v1/assistant/conversations/${encodeURIComponent(scope)}`;
  return chat ? `${path}/chats/${encodeURIComponent(chat)}` : path;
};

export const getAssistantSettings = (): Promise<AssistantSettings> =>
  request("/api/v1/assistant/settings");

export const saveAssistantSettings = (settings: AssistantSettings): Promise<AssistantSettings> =>
  request("/api/v1/assistant/settings", { method: "PUT", body: settings });

export const getConversation = (scope: ChatTarget): Promise<Conversation> =>
  request(conversationPath(scope));

export const sendToAssistant = (
  scope: ChatTarget,
  message: string,
  key: string,
  attachments: string[] = [],
): Promise<Conversation> =>
  request(`${conversationPath(scope)}/messages`, {
    method: "POST",
    body: {
      message,
      developer_mode: developerModeOn(),
      ...(attachments.length ? { attachments } : {}),
    },
    idempotencyKey: key,
  });

/** Runs again the turn whose automatic retries ran out. */
export const retryAssistantTurn = (scope: ChatTarget): Promise<Conversation> =>
  request(`${conversationPath(scope)}/retry`, { method: "POST" });

/** Focus the conversation on an item, or clear the focus with null. */
export const setAssistantFocus = (
  scope: ChatTarget,
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
  scope: ChatTarget,
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

export const dismissAssistantAction = (
  scope: ChatTarget,
  actionId: string,
): Promise<Conversation> =>
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
/** A scope's open chats, the most recently used first. */
export const listChats = (scope: string): Promise<ChatSummary[]> =>
  request<{ chats: ChatSummary[] }>(`${conversationPath(scope)}/chats`).then((r) => r.chats);

/** Starts a chat in a scope; it is named after its first message. */
export const createChat = (scope: string): Promise<Conversation> =>
  request(`${conversationPath(scope)}/chats`, { method: "POST" });

/** Makes a loose chat a project (ADR-038): the chat moves into the new
 * project as its first chat, and its planning starts from the chat. */
export const makeChatProject = (chat: string): Promise<Conversation> =>
  request(`${conversationPath({ scope: "commitarium", chat })}/project`, { method: "POST" });

/** Answers a question of the assistant's with one of its options or the
 * user's own words; the answer goes into the chat as the user's message. */
export const answerAssistantQuestion = (
  target: { scope: string; chat: string },
  messageId: string,
  questionId: string,
  answer: { option: number } | { text: string },
): Promise<Conversation> =>
  request(
    `${conversationPath(target)}/messages/${encodeURIComponent(messageId)}/questions/${encodeURIComponent(questionId)}/answer`,
    { method: "POST", body: answer },
  );

/** Answers several questions of the assistant's message at once; the
 * answers go into the chat as one message of the user's, so the assistant
 * hears them all in one turn. */
export const answerAssistantQuestions = (
  target: { scope: string; chat: string },
  messageId: string,
  answers: ({ question: string } & ({ option: number } | { text: string }))[],
): Promise<Conversation> =>
  request(`${conversationPath(target)}/messages/${encodeURIComponent(messageId)}/answers`, {
    method: "POST",
    body: { answers },
  });

export const startLaunch = (scope: string): Promise<Conversation> =>
  request(`${conversationPath(scope)}/launch`, { method: "POST" });
