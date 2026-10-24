import { useCallback, useEffect, useRef, useState } from "react";
import {
  approveAssistantAction,
  dismissAssistantAction,
  getAssistantSettings,
  type AssistantSettings,
  getConversation,
  saveAssistantNotes,
  sendToAssistant,
  type AssistantAction,
  type AutopilotGoal,
  type LaunchStage,
  type Conversation,
} from "../api/assistant";
import { ApiError } from "../api/client";
import { openAppWindow } from "../ipc";
import { getVision } from "../api/vision";
import { getProject } from "../api/projects";
import type { AssistantMode, VisionItem, VisionPriority } from "../api/types";
import { appliesItself, assistantMode, assistantModeNote } from "./assistantMode";
import { Markdown } from "./Markdown";
import { useCatalogModels, useModelName } from "./useModelName";
import { agentName, reloadAgents, useAgentList, useAgents } from "./useAgents";
import type { AgentRole } from "../api/types";
import { Agents } from "./Agents";
import { AgentLogin } from "./AgentLogin";
import { assistantGuidance, GUIDANCE } from "./setupGuidance";
import { appliedWhat, chooseAgent, choiceOverrides, type RoleChoice } from "./actionCard";

const IDLE_POLL_MS = 20_000;
const POLL_MS = 2000;

/** Fired when an approved assistant action changed the project's work. */
export const WORK_CHANGED = "commitarium:work-changed";
/** Asks the open project to show its Vision page. */
export const OPEN_VISION = "commitarium:open-vision";
/** Opens the assistant, optionally with a message started in its composer. */
export const OPEN_ASSISTANT = "commitarium:open-assistant";
/** Asks the assistant to reload its conversation, as after autopilot starts or stops. */
export const ASSISTANT_CHANGED = "commitarium:assistant-changed";

const STEPS_LABEL: Record<string, string> = {
  full: "Full (plan, review)",
  planned: "Planned (no reviewer)",
  quick: "Quick change",
};

// The assistant (ASSISTANT_PLAN.md) in a drawer on the right of the window,
// opened from a round button in the top-right corner and minimized back to
// it, so the user keeps their place. Docked, the page narrows beside it;
// otherwise it lies over the page. It follows where the user is: a
// project's conversation inside a project, the top-level one elsewhere. The
// button pulses while it answers and shows a dot for a reply not yet seen.
export function AssistantDrawer({
  scope,
  scopeLabel,
  onOpenOrder,
  open,
  onOpenChange: setOpen,
  docked,
  onGoalChange,
  onLaunchChange,
}: {
  scope: string;
  scopeLabel: string;
  onOpenOrder?: (featureId: string) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Beside the page rather than over it. */
  docked: boolean;
  /** The conversation's autopilot goal while it is on, else null. */
  onGoalChange?: (goal: AutopilotGoal | null) => void;
  /** A new app's launch stage while it is planned or set up, else null. */
  onLaunchChange?: (stage: LaunchStage | null) => void;
}) {
  const [conversation, setConversationState] = useState<Conversation | null>(null);
  // Work autopilot changed reloads the project's lists, as the user's own
  // approvals do.
  const autoApplied = useRef<number | null>(null);
  const setConversation = useCallback(
    (next: Conversation | null) => {
      setConversationState(next);
      onGoalChange?.(next?.goal?.status === "active" ? next.goal : null);
      const stage = next?.launch?.stage;
      onLaunchChange?.(stage === "vision" || stage === "setup" ? stage : null);
      const applied =
        next?.actions.filter((a) => a.auto_tried && a.status === "applied").length ?? null;
      if (applied !== null && autoApplied.current !== null && applied > autoApplied.current) {
        window.dispatchEvent(new Event(WORK_CHANGED));
      }
      autoApplied.current = applied;
    },
    [onGoalChange, onLaunchChange],
  );
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [notesOpen, setNotesOpen] = useState(false);
  const [unseen, setUnseen] = useState(false);
  const chatRef = useRef<HTMLDivElement | null>(null);
  const composerRef = useRef<HTMLTextAreaElement | null>(null);
  // Set when the drawer is opened with a started message ("I'd like a new
  // app: "), so the cursor lands after it, ready to type.
  const caretToEnd = useRef(false);
  const seenReplies = useRef<number | null>(null);

  // What the user must set up before the assistant can answer (ADR-027): an
  // agent, then the assistant's choice of one. The Agents page opens over the
  // drawer, so it works in an app's own window too.
  const agents = useAgentList();
  const [settings, setSettings] = useState<AssistantSettings | null>(null);
  const guidance = assistantGuidance(agents, settings);
  const [setUp, setSetUp] = useState<null | "agents" | "assistant">(null);

  // How the assistant works in the project (ADR-027). Where it builds
  // everything, work it applies by itself shows as a line, not a card to
  // approve. Read when the scope changes and when the assistant reloads.
  const [mode, setMode] = useState<AssistantMode | null>(null);
  const loadMode = useCallback(() => {
    if (scope === "commitarium") {
      setMode(null);
      return;
    }
    getProject(scope)
      .then((project) => setMode(assistantMode(project)))
      .catch(() => undefined);
  }, [scope]);
  useEffect(() => {
    setMode(null);
    loadMode();
    window.addEventListener(ASSISTANT_CHANGED, loadMode);
    return () => window.removeEventListener(ASSISTANT_CHANGED, loadMode);
  }, [loadMode]);
  const builds = mode === "builds";

  // Vision items by ID, labelled "P0.2 Title", for the cards.
  const [visionItems, setVisionItems] = useState<Map<string, string>>(new Map());
  const load = useCallback(async () => {
    getAssistantSettings()
      .then(setSettings)
      .catch(() => undefined);
    try {
      setConversation(await getConversation(scope));
      setError(null);
    } catch (e) {
      setError(describe(e));
    }
    if (scope !== "commitarium") {
      getVision(scope)
        .then((vision) => setVisionItems(visionLabels(vision.plan.priorities)))
        .catch(() => setVisionItems(new Map()));
    }
  }, [scope, setConversation]);

  useEffect(() => {
    const reload = () => void load();
    window.addEventListener(ASSISTANT_CHANGED, reload);
    return () => window.removeEventListener(ASSISTANT_CHANGED, reload);
  }, [load]);

  useEffect(() => {
    const openWith = (event: Event) => {
      const started = (event as CustomEvent<{ draft?: string }>).detail?.draft;
      if (started) {
        setDraft(started);
        caretToEnd.current = true;
      }
      setOpen(true);
    };
    window.addEventListener(OPEN_ASSISTANT, openWith);
    return () => window.removeEventListener(OPEN_ASSISTANT, openWith);
  }, [setOpen]);

  useEffect(() => {
    const composer = composerRef.current;
    if (!caretToEnd.current || !open || !composer) return;
    caretToEnd.current = false;
    composer.focus();
    composer.setSelectionRange(draft.length, draft.length);
  }, [draft, open]);

  useEffect(() => {
    setConversation(null);
    setNotesOpen(false);
    setUnseen(false);
    seenReplies.current = null;
    void load();
  }, [load, setConversation]);

  const running = conversation?.status === "running";
  // Quickly while it answers; slowly otherwise, for the turns that project
  // events start on their own.
  useEffect(() => {
    const timer = setInterval(() => void load(), running ? POLL_MS : IDLE_POLL_MS);
    return () => clearInterval(timer);
  }, [running, load]);

  // A reply that arrives while the drawer is closed marks the button.
  const replies = conversation?.messages.filter((m) => m.role !== "user").length ?? 0;
  useEffect(() => {
    if (!conversation) return;
    if (seenReplies.current === null || open) {
      seenReplies.current = replies;
      if (open) setUnseen(false);
      return;
    }
    if (replies > seenReplies.current) setUnseen(true);
  }, [conversation, replies, open]);

  const count = (conversation?.messages.length ?? 0) + (conversation?.queued?.length ?? 0);
  useEffect(() => {
    const el = chatRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [count, running, open, notesOpen]);

  const send = async () => {
    const text = draft.trim();
    if (!text) return;
    setSending(true);
    setError(null);
    try {
      setConversation(await sendToAssistant(scope, text, crypto.randomUUID()));
      setDraft("");
    } catch (e) {
      if (e instanceof ApiError && (e.code === "no_agents" || e.code === "assistant_not_set_up")) {
        // The guidance shows what to set up; the message waits in the box.
        void reloadAgents().catch(() => undefined);
        void load();
      } else {
        setError(describe(e));
      }
    } finally {
      setSending(false);
    }
  };

  const byMessage = new Map<string, AssistantAction[]>();
  for (const action of conversation?.actions ?? []) {
    byMessage.set(action.message_id, [...(byMessage.get(action.message_id) ?? []), action]);
  }

  return (
    <>
      {/* One button in the corner opens and closes the drawer. */}
      <button
        className={`assistant-fab ${open ? "assistant-fab--open" : ""} ${running && !open ? "assistant-fab--working" : ""}`}
        onClick={() => setOpen(!open)}
        title={
          open
            ? "Close the assistant"
            : running
              ? "The assistant is answering"
              : "Open the assistant"
        }
        aria-label={open ? "Close the assistant" : "Open the assistant"}
        aria-expanded={open}
      >
        {open ? (
          <svg viewBox="0 0 24 24" width="18" height="18" fill="none" aria-hidden>
            <path
              d="M6 6l12 12M18 6L6 18"
              stroke="currentColor"
              strokeWidth="2"
              strokeLinecap="round"
            />
          </svg>
        ) : (
          <svg viewBox="0 0 24 24" width="20" height="20" fill="none" aria-hidden>
            <path
              d="M12 3l1.8 4.6L18.5 9l-4.7 1.6L12 15l-1.8-4.4L5.5 9l4.7-1.4L12 3zM18 15l.9 2.1 2.1.9-2.1.9L18 21l-.9-2.1L15 18l2.1-.9L18 15z"
              fill="currentColor"
            />
          </svg>
        )}
        {unseen && !open && (
          <span className="assistant-fab__dot" aria-label="A reply you have not seen" />
        )}
      </button>
      {open && (
        <aside
          className={`assistant-drawer ${docked ? "assistant-drawer--docked" : ""}`}
          aria-label="Assistant"
        >
          <header className="assistant-drawer__head">
            <div className="assistant-drawer__title">
              <strong>Assistant</strong>
              <span className="muted">{scopeLabel}</span>
            </div>
            {mode && (
              <span
                className="pill assistant-drawer__mode"
                title={
                  builds
                    ? "It starts and finishes its own work without asking. Change this in the project's settings."
                    : "It suggests; nothing happens until you approve. Change this in the project's settings."
                }
              >
                {assistantModeNote(mode)}
              </span>
            )}
            <button
              className={`ghost ${notesOpen ? "ghost--on" : ""}`}
              onClick={() => setNotesOpen((v) => !v)}
            >
              Notes
            </button>
          </header>
          {!conversation ? (
            error ? (
              <div className="banner banner--error">{error}</div>
            ) : (
              <p className="muted">Loading…</p>
            )
          ) : notesOpen ? (
            <Notes scope={scope} notes={conversation.notes} onSaved={setConversation} />
          ) : (
            <>
              <div className="chat assistant-drawer__chat" ref={chatRef}>
                {conversation.messages.length === 0 && (
                  <p className="muted assistant__empty">
                    {scope === "commitarium"
                      ? "Ask about your projects. Open a project to have it draft work there."
                      : builds
                        ? "Ask for a change and it builds it, along with work orders you add yourself. It asks before premium models."
                        : "Ask about the project, what to build next, or have it draft work orders. It proposes; nothing happens until you approve."}
                  </p>
                )}
                {conversation.messages.map((message) =>
                  message.role === "event" ? (
                    <p key={message.id} className="muted assistant__event">
                      {message.text}
                    </p>
                  ) : (
                    <div key={message.id} className="assistant__turn">
                      <div className={`msg ${message.role === "user" ? "msg--user" : "msg--lead"}`}>
                        <span className="msg__who">
                          {message.role === "user" ? "You" : "Assistant"}
                        </span>
                        <div className="msg__text">
                          <Markdown text={message.text} />
                        </div>
                      </div>
                      {(byMessage.get(message.id) ?? []).map((action) =>
                        builds && appliesItself(action) ? (
                          <AppliedLine key={action.id} action={action} />
                        ) : (
                          <ActionCard
                            key={action.id}
                            scope={scope}
                            action={action}
                            onChanged={setConversation}
                            onOpenOrder={onOpenOrder}
                            visionLabel={visionItems.get(action.vision_item_id)}
                          />
                        ),
                      )}
                    </div>
                  ),
                )}
                {guidance && (
                  <div className="banner banner--info assistant__setup">
                    <span>{GUIDANCE[guidance].text}</span>
                    <button
                      className="primary"
                      onClick={() => setSetUp(guidance === "add_agent" ? "agents" : "assistant")}
                    >
                      {GUIDANCE[guidance].action}
                    </button>
                  </div>
                )}
                {running && <p className="muted status-line">The assistant is thinking…</p>}
                {!running && conversation.sign_in?.needs_user && (
                  <SignInNotice
                    agentId={conversation.sign_in.agent}
                    agentName={agentName(agents ?? [], conversation.sign_in.agent)}
                  />
                )}
                {(conversation.queued ?? []).map((message) => (
                  <div key={message.id} className="assistant__turn assistant__turn--queued">
                    <div className="msg msg--user">
                      <span className="msg__who">You · queued</span>
                      <div className="msg__text">
                        <Markdown text={message.text} />
                      </div>
                    </div>
                    <p className="muted note assistant__queued-note">
                      The assistant answers when it's free.
                    </p>
                  </div>
                ))}
              </div>
              {error && <div className="banner banner--error">{error}</div>}
              <div className="composer">
                <div className="reply">
                  <textarea
                    ref={composerRef}
                    rows={2}
                    value={draft}
                    autoFocus
                    placeholder={
                      running
                        ? "The assistant is busy; write anyway, it answers next…"
                        : "Ask the assistant…"
                    }
                    onChange={(e) => setDraft(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Escape") setOpen(false);
                      if (e.key !== "Enter" || e.shiftKey) return;
                      e.preventDefault();
                      if (!sending && !guidance && draft.trim()) void send();
                    }}
                    disabled={sending}
                  />
                  <button
                    onClick={() => void send()}
                    disabled={sending || !!guidance || !draft.trim()}
                  >
                    {sending ? "Sending…" : "Send"}
                  </button>
                </div>
              </div>
            </>
          )}
        </aside>
      )}
      {setUp && (
        <Agents
          focus={setUp === "assistant" ? "assistant" : undefined}
          onClose={() => {
            setSetUp(null);
            void reloadAgents().catch(() => undefined);
            void load();
          }}
        />
      )}
    </>
  );
}

function ActionCard({
  scope,
  action,
  onChanged,
  onOpenOrder,
  visionLabel,
}: {
  scope: string;
  action: AssistantAction;
  /** "P0.2 Title" for the vision item a new work order delivers. */
  visionLabel?: string;
  onChanged: (conversation: Conversation) => void;
  onOpenOrder?: (featureId: string) => void;
}) {
  const modelName = useModelName();
  const agents = useAgents();
  const modelsFor = useCatalogModels();
  const askBefore = useAskBefore();
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState<null | "approve" | "start" | "dismiss">(null);
  const [error, setError] = useState<string | null>(null);
  const pending = action.status === "proposed";
  // A new or started work order's agents and models are the user's to
  // change before approving (ADR-027); another agent moves its model to one
  // its provider offers. Premium is judged on what they choose.
  const choosable =
    pending && (action.type === "create_work_order" || action.type === "start_work_order");
  const proposed: Record<AgentRole, RoleChoice> = {
    lead: { agent: action.lead_agent, model: action.lead_model },
    reviewer: { agent: action.reviewer_agent, model: action.reviewer_model },
  };
  const [chosen, setChosen] = useState(proposed);
  const roles = (["lead", "reviewer"] as const).filter((role) => action[`${role}_model`]);
  const premium = choosable
    ? roles.map((role) => chosen[role].model).filter((model) => isPremium(model, askBefore))
    : (action.premium ?? []);
  // An agent whose models are not known yet leaves its model to choose.
  const unchosen = choosable && roles.some((role) => !chosen[role].model);

  const run = async (kind: NonNullable<typeof busy>, fn: () => Promise<Conversation>) => {
    setBusy(kind);
    setError(null);
    try {
      const conversation = await fn();
      onChanged(conversation);
      if (kind !== "dismiss") {
        // An approved action may have created or started work: the open
        // project's lists reload, and the app goes to what changed.
        window.dispatchEvent(new Event(WORK_CHANGED));
        const applied = conversation.actions?.find((a) => a.id === action.id);
        if (applied?.type === "create_app" && applied.project_id) {
          void openAppWindow(applied.project_id, applied.title);
        } else if (applied?.type === "add_vision_item") {
          window.dispatchEvent(new Event(OPEN_VISION));
        } else if (applied?.work_order_id) {
          onOpenOrder?.(applied.work_order_id);
        }
      }
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
    }
  };
  const approve = (start: boolean) =>
    run(start ? "start" : "approve", () =>
      approveAssistantAction(scope, action.id, {
        start,
        confirm_premium: confirmed,
        ...(choosable ? choiceOverrides(proposed, chosen) : {}),
      }),
    );

  const heading =
    action.type === "create_work_order"
      ? `New work order: ${action.title}`
      : action.type === "start_work_order"
        ? "Start a work order"
        : action.type === "add_vision_item"
          ? `Add to the vision as P${action.priority}: ${action.title}`
          : action.type === "start_autopilot"
            ? `Autopilot: finish P${action.priority}`
            : action.type === "continue_work_order"
              ? "Continue a work order"
              : action.type === "close_work_order"
                ? "Close a work order"
                : action.type === "create_app"
                  ? `New app: ${action.title}`
                  : action.type === "set_up_preview"
                    ? "Set up the app's preview"
                    : action.type === "write_vision"
                      ? "Write the vision"
                      : action.type === "set_up_stack"
                        ? "Set up the stack"
                        : "Switch models";
  const models = [
    action.lead_model &&
      `Lead ${action.lead_agent ? agentName(agents, action.lead_agent) + " · " : ""}${modelName(action.lead_model)}`,
    action.reviewer_model &&
      `Reviewer ${action.reviewer_agent ? agentName(agents, action.reviewer_agent) + " · " : ""}${modelName(action.reviewer_model)}`,
  ].filter(Boolean);
  const blocked = (premium.length > 0 && !confirmed) || unchosen;

  return (
    <div className={`assistant-action assistant-action--${action.status}`}>
      <div className="assistant-action__head">
        <strong>{heading}</strong>
        {!pending && <span className="pill">{STATUS_LABEL[action.status]}</span>}
      </div>
      {action.description && <p className="assistant-action__desc">{action.description}</p>}
      {action.type === "continue_work_order" && (
        <p className="assistant-action__desc">
          {action.guidance ? `Guidance: ${action.guidance}` : "Retries without guidance."}
        </p>
      )}
      {action.type === "set_up_preview" && (
        <p className="assistant-action__desc">
          An agent adjusts the app&apos;s setup; it merges once Commitarium has started the app for
          real.{action.guidance ? ` What fails: ${action.guidance}` : ""}
        </p>
      )}
      <ul className="assistant-action__facts">
        {action.steps && <li>{STEPS_LABEL[action.steps]}</li>}
        {choosable
          ? roles.map((role) => {
              const label = role === "lead" ? "Lead" : "Reviewer";
              const current = chosen[role];
              const provider = agents.find((agent) => agent.id === current.agent)?.provider;
              const options = provider ? modelsFor(provider, role) : [];
              return (
                <li key={role} className="assistant-action__model">
                  {label}{" "}
                  {agents.length > 1 ? (
                    <select
                      value={current.agent}
                      onChange={(e) =>
                        setChosen((c) => ({
                          ...c,
                          [role]: chooseAgent(
                            c[role],
                            e.target.value,
                            agents,
                            modelsFor,
                            role,
                            askBefore,
                          ),
                        }))
                      }
                      disabled={busy !== null}
                      aria-label={`${label} agent`}
                    >
                      {!agents.some((agent) => agent.id === current.agent) && (
                        <option value={current.agent}>{current.agent}</option>
                      )}
                      {agents.map((agent) => (
                        <option key={agent.id} value={agent.id}>
                          {agent.name}
                        </option>
                      ))}
                    </select>
                  ) : (
                    agentName(agents, current.agent)
                  )}
                  {" · "}
                  {options.length > 0 ? (
                    <select
                      value={current.model}
                      onChange={(e) =>
                        setChosen((c) => ({ ...c, [role]: { ...c[role], model: e.target.value } }))
                      }
                      disabled={busy !== null}
                      aria-label={`${label} model`}
                    >
                      {!options.some((m) => m.id === current.model) && (
                        <option value={current.model}>
                          {current.model ? modelName(current.model) : "Choose a model"}
                        </option>
                      )}
                      {options.map((m) => (
                        <option key={m.id} value={m.id}>
                          {m.display_name}
                        </option>
                      ))}
                    </select>
                  ) : current.model ? (
                    modelName(current.model)
                  ) : (
                    <span className="muted">its models are not known yet</span>
                  )}
                </li>
              );
            })
          : models.map((line) => <li key={line as string}>{line}</li>)}
        {action.independent_tests && <li>Independent acceptance tests</li>}
        {action.type === "create_work_order" && (
          <li>
            {action.vision_item_id
              ? `Part of the vision: ${visionLabel ?? action.vision_item_id}`
              : "Outside the vision"}
          </li>
        )}
        {action.work_order_id && (
          <li>
            Work order{" "}
            {onOpenOrder ? (
              <button className="linkish" onClick={() => onOpenOrder(action.work_order_id)}>
                {action.work_order_id}
              </button>
            ) : (
              action.work_order_id
            )}
          </li>
        )}
        {action.after && (
          <li>
            {action.queued
              ? `Queued: starts once ${action.after} has merged`
              : `Best after ${action.after}: it changes the same part of the app`}
          </li>
        )}
        {action.type === "set_up_stack" && (
          <li>The stack the vision recommends, or the one the code already uses</li>
        )}
        {action.when && (
          <li>{action.when === "now" ? "Stop now and switch" : "From the next turn"}</li>
        )}
        {action.type === "start_autopilot" && (
          <li>
            The assistant creates and starts work orders for P{action.priority}'s open items without
            asking, except with premium models. You can stop it at any time.
          </li>
        )}
        {action.auto_tried && action.status === "applied" && <li>Applied by autopilot</li>}
      </ul>
      <p className="muted assistant-action__reason">Why: {action.reason}</p>
      {action.result && <p className="muted">{action.result}</p>}
      {pending && premium.length > 0 && (
        <label className="toggle assistant-action__premium">
          <input
            type="checkbox"
            checked={confirmed}
            onChange={(e) => setConfirmed(e.target.checked)}
          />
          Use {premium.map(modelName).join(" and ")} — a model you asked to approve first
        </label>
      )}
      {error && <div className="banner banner--error">{error}</div>}
      {pending && (
        <div className="row">
          {action.type === "create_work_order" ? (
            <>
              <button
                className="primary"
                onClick={() => void approve(true)}
                disabled={busy !== null || blocked}
              >
                {busy === "start" ? "Starting…" : "Create and start"}
              </button>
              <button onClick={() => void approve(false)} disabled={busy !== null || blocked}>
                {busy === "approve" ? "Creating…" : "Create as draft"}
              </button>
            </>
          ) : (
            <button
              className="primary"
              onClick={() => void approve(false)}
              disabled={busy !== null || blocked}
            >
              {busy === "approve" ? "Applying…" : APPLY_LABEL[action.type]}
            </button>
          )}
          <button
            className="ghost"
            onClick={() => void run("dismiss", () => dismissAssistantAction(scope, action.id))}
            disabled={busy !== null}
          >
            Dismiss
          </button>
        </div>
      )}
    </div>
  );
}

const APPLY_LABEL: Record<AssistantAction["type"], string> = {
  create_work_order: "Create",
  start_work_order: "Start",
  change_models: "Switch",
  add_vision_item: "Add to vision",
  start_autopilot: "Start autopilot",
  continue_work_order: "Continue",
  close_work_order: "Close",
  create_app: "Create app",
  set_up_preview: "Set up preview",
  write_vision: "Write vision",
  set_up_stack: "Set up stack",
};

const STATUS_LABEL: Record<AssistantAction["status"], string> = {
  proposed: "Proposed",
  applied: "Done",
  dismissed: "Dismissed",
  failed: "Failed",
};

function Notes({
  scope,
  notes,
  onSaved,
}: {
  scope: string;
  notes: string;
  onSaved: (conversation: Conversation) => void;
}) {
  const [editing, setEditing] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const save = async () => {
    if (editing === null) return;
    setSaving(true);
    setError(null);
    try {
      onSaved(await saveAssistantNotes(scope, editing));
      setEditing(null);
    } catch (e) {
      setError(describe(e));
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="assistant-drawer__notes">
      <p className="muted note">
        What the assistant keeps for itself: decisions, your preferences, and reusable work. You can
        edit them.
      </p>
      {editing === null ? (
        <>
          {notes ? <Markdown text={notes} /> : <p className="muted">No notes yet.</p>}
          <button className="ghost" onClick={() => setEditing(notes)}>
            Edit
          </button>
        </>
      ) : (
        <>
          <textarea
            className="assistant__notes-edit"
            rows={14}
            value={editing}
            onChange={(e) => setEditing(e.target.value)}
            disabled={saving}
          />
          {error && <div className="banner banner--error">{error}</div>}
          <div className="row">
            <button className="primary" onClick={() => void save()} disabled={saving}>
              {saving ? "Saving…" : "Save"}
            </button>
            <button className="ghost" onClick={() => setEditing(null)} disabled={saving}>
              Cancel
            </button>
          </div>
        </>
      )}
    </div>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === "assistant_unavailable") return "The assistant is unavailable: " + e.message;
    return e.message;
  }
  return String(e);
}

function visionLabels(priorities: VisionPriority[]): Map<string, string> {
  const labels = new Map<string, string>();
  const walk = (items: VisionItem[], prefix: string) =>
    items.forEach((item, index) => {
      const label = `${prefix}.${index + 1}`;
      labels.set(item.id, `${label} ${item.title}`);
      walk(item.items ?? [], label);
    });
  priorities.forEach((p) => walk(p.items, `P${p.level}`));
  return labels;
}

// The model families the user asked to approve first, fetched once.
let askBeforeCache: Promise<string[]> | null = null;

function useAskBefore(): string[] {
  const [families, setFamilies] = useState<string[]>(["fable", "astra"]);
  useEffect(() => {
    let active = true;
    askBeforeCache ??= getAssistantSettings()
      .then((settings) => settings.ask_before)
      .catch(() => {
        askBeforeCache = null;
        return ["fable", "astra"];
      });
    void askBeforeCache.then((loaded) => active && setFamilies(loaded));
    return () => {
      active = false;
    };
  }, []);
  return families;
}

function isPremium(model: string, families: string[]): boolean {
  const name = model.toLowerCase();
  return !!name && families.some((family) => family && name.includes(family.toLowerCase()));
}

// Where it builds everything, what the assistant set going, said in a line.
function AppliedLine({ action }: { action: AssistantAction }) {
  const what =
    appliedWhat(action.title) ||
    (action.type === "set_up_preview"
      ? "the app's setup"
      : action.type === "set_up_stack"
        ? "the stack"
        : action.type === "write_vision"
          ? "the vision"
          : "the work");
  const failed = action.status === "failed";
  const done = action.status === "applied";
  const [verb, past] =
    action.type === "change_models"
      ? ["Switching models", "Switched models"]
      : action.type === "continue_work_order"
        ? ["Picking the work back up", "Picked the work back up"]
        : action.type === "set_up_preview"
          ? ["Fixing", "Fixing"]
          : action.type === "write_vision"
            ? ["Writing", "Writing"]
            : action.type === "set_up_stack"
              ? ["Setting up", "Setting up"]
              : action.hold_start
                ? ["Creating", "Created"]
                : ["Starting", "Started"];
  return (
    <p className={`muted assistant__applied ${failed ? "assistant__applied--failed" : ""}`}>
      {failed
        ? `Couldn't start: ${what}`
        : done
          ? `✓ ${past}: ${what}`
          : action.queued
            ? `Queued: ${what}, after ${action.after}`
            : `${verb}: ${what}…`}
    </p>
  );
}

/** The assistant's last turn waits for its agent's login: sign in here, and
 * the kept message is sent (LOGIN_RECOVERY_PLAN.md). */
export function SignInNotice({ agentId, agentName }: { agentId: string; agentName: string }) {
  return (
    <div className="wait-banner wait-banner--warn wait-banner--login">
      <span className="wait-banner__kind">{agentName} needs you to sign in again</span>
      <span>Your message is kept and is sent as soon as {agentName} works again.</span>
      <AgentLogin
        agentId={agentId}
        agentName={agentName}
        continuing="the assistant is picking your message back up"
      />
    </div>
  );
}
