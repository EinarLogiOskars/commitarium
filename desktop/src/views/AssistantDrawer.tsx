import { useCallback, useEffect, useRef, useState } from "react";
import {
  approveAssistantAction,
  dismissAssistantAction,
  getAssistantSettings,
  getConversation,
  saveAssistantNotes,
  sendToAssistant,
  type AssistantAction,
  type AutopilotGoal,
  type Conversation,
} from "../api/assistant";
import { ApiError } from "../api/client";
import { getVision } from "../api/vision";
import type { VisionItem, VisionPriority } from "../api/types";
import { Markdown } from "./Markdown";
import { useCatalogModels, useModelName } from "./useModelName";
import { agentName, useAgents } from "./useAgents";
import type { AgentRole } from "../api/types";

const IDLE_POLL_MS = 20_000;
const POLL_MS = 2000;

/** Fired when an approved assistant action changed the project's work. */
export const WORK_CHANGED = "commitarium:work-changed";
/** Asks the open project to show its Vision page. */
export const OPEN_VISION = "commitarium:open-vision";
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
}) {
  const [conversation, setConversationState] = useState<Conversation | null>(null);
  // Work autopilot changed reloads the project's lists, as the user's own
  // approvals do.
  const autoApplied = useRef<number | null>(null);
  const setConversation = useCallback(
    (next: Conversation | null) => {
      setConversationState(next);
      onGoalChange?.(next?.goal?.status === "active" ? next.goal : null);
      const applied =
        next?.actions.filter((a) => a.auto_tried && a.status === "applied").length ?? null;
      if (applied !== null && autoApplied.current !== null && applied > autoApplied.current) {
        window.dispatchEvent(new Event(WORK_CHANGED));
      }
      autoApplied.current = applied;
    },
    [onGoalChange],
  );
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [notesOpen, setNotesOpen] = useState(false);
  const [unseen, setUnseen] = useState(false);
  const chatRef = useRef<HTMLDivElement | null>(null);
  const seenReplies = useRef<number | null>(null);

  // Vision items by ID, labelled "P0.2 Title", for the cards.
  const [visionItems, setVisionItems] = useState<Map<string, string>>(new Map());
  const load = useCallback(async () => {
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

  const count = conversation?.messages.length ?? 0;
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
      setError(describe(e));
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
                      {(byMessage.get(message.id) ?? []).map((action) => (
                        <ActionCard
                          key={action.id}
                          scope={scope}
                          action={action}
                          onChanged={setConversation}
                          onOpenOrder={onOpenOrder}
                          visionLabel={visionItems.get(action.vision_item_id)}
                        />
                      ))}
                    </div>
                  ),
                )}
                {running && <p className="muted status-line">The assistant is thinking…</p>}
              </div>
              {error && <div className="banner banner--error">{error}</div>}
              <div className="composer">
                <div className="reply">
                  <textarea
                    rows={2}
                    value={draft}
                    autoFocus
                    placeholder="Ask the assistant…"
                    onChange={(e) => setDraft(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Escape") setOpen(false);
                      if (e.key !== "Enter" || e.shiftKey) return;
                      e.preventDefault();
                      if (!running && !sending && draft.trim()) void send();
                    }}
                    disabled={sending}
                  />
                  <button
                    onClick={() => void send()}
                    disabled={running || sending || !draft.trim()}
                  >
                    {sending ? "Sending…" : "Send"}
                  </button>
                </div>
              </div>
            </>
          )}
        </aside>
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
  // A new or started work order's models are the user's to change before
  // approving; premium is judged on what they choose.
  const choosable =
    pending && (action.type === "create_work_order" || action.type === "start_work_order");
  const [chosen, setChosen] = useState<Record<AgentRole, string>>({
    lead: action.lead_model,
    reviewer: action.reviewer_model,
  });
  const premium = choosable
    ? [chosen.lead, chosen.reviewer].filter((model) => isPremium(model, askBefore))
    : (action.premium ?? []);

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
        if (applied?.type === "add_vision_item") {
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
        ...(choosable && chosen.lead !== action.lead_model ? { lead_model: chosen.lead } : {}),
        ...(choosable && chosen.reviewer !== action.reviewer_model
          ? { reviewer_model: chosen.reviewer }
          : {}),
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
                : "Switch models";
  const models = [
    action.lead_model &&
      `Lead ${action.lead_agent ? agentName(agents, action.lead_agent) + " · " : ""}${modelName(action.lead_model)}`,
    action.reviewer_model &&
      `Reviewer ${action.reviewer_agent ? agentName(agents, action.reviewer_agent) + " · " : ""}${modelName(action.reviewer_model)}`,
  ].filter(Boolean);
  const blocked = premium.length > 0 && !confirmed;

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
      <ul className="assistant-action__facts">
        {action.steps && <li>{STEPS_LABEL[action.steps]}</li>}
        {choosable
          ? (["lead", "reviewer"] as const)
              .filter((role) => action[`${role}_model`])
              .map((role) => {
                const agentId = action[`${role}_agent`];
                const provider = agents.find((agent) => agent.id === agentId)?.provider;
                const options = provider ? modelsFor(provider, role) : [];
                return (
                  <li key={role} className="assistant-action__model">
                    {role === "lead" ? "Lead" : "Reviewer"}{" "}
                    {agentId ? agentName(agents, agentId) + " · " : ""}
                    {options.length > 0 ? (
                      <select
                        value={chosen[role]}
                        onChange={(e) => setChosen((c) => ({ ...c, [role]: e.target.value }))}
                        disabled={busy !== null}
                        aria-label={`${role === "lead" ? "Lead" : "Reviewer"} model`}
                      >
                        {!options.some((m) => m.id === chosen[role]) && (
                          <option value={chosen[role]}>{modelName(chosen[role])}</option>
                        )}
                        {options.map((m) => (
                          <option key={m.id} value={m.id}>
                            {m.display_name}
                          </option>
                        ))}
                      </select>
                    ) : (
                      modelName(chosen[role])
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
