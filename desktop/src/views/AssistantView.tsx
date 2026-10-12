import { useCallback, useEffect, useRef, useState } from "react";
import {
  approveAssistantAction,
  dismissAssistantAction,
  getConversation,
  saveAssistantNotes,
  sendToAssistant,
  type AssistantAction,
  type Conversation,
} from "../api/assistant";
import { ApiError } from "../api/client";
import { Markdown } from "./Markdown";
import { useModelName } from "./useModelName";
import { agentName, useAgents } from "./useAgents";

const POLL_MS = 2000;

const STEPS_LABEL: Record<string, string> = {
  full: "Full (plan, review)",
  planned: "Planned (no reviewer)",
  quick: "Quick change",
};

// The project's assistant (ASSISTANT_PLAN.md): one continuous conversation in
// which it answers and proposes actions. Each action is a card the user
// approves; a premium model needs an explicit tick first.
export function AssistantView({
  scope,
  onOpenOrder,
}: {
  scope: string;
  onOpenOrder?: (featureId: string) => void;
}) {
  const [conversation, setConversation] = useState<Conversation | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [notesOpen, setNotesOpen] = useState(false);
  const chatRef = useRef<HTMLDivElement | null>(null);

  const load = useCallback(async () => {
    try {
      setConversation(await getConversation(scope));
      setError(null);
    } catch (e) {
      setError(describe(e));
    }
  }, [scope]);

  useEffect(() => {
    setConversation(null);
    void load();
  }, [load]);

  const running = conversation?.status === "running";
  useEffect(() => {
    if (!running) return;
    const timer = setInterval(() => void load(), POLL_MS);
    return () => clearInterval(timer);
  }, [running, load]);

  const count = conversation?.messages.length ?? 0;
  useEffect(() => {
    const el = chatRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [count, running]);

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

  if (!conversation) {
    return error ? (
      <div className="banner banner--error">{error}</div>
    ) : (
      <p className="muted">Loading…</p>
    );
  }

  const byMessage = new Map<string, AssistantAction[]>();
  for (const action of conversation.actions) {
    byMessage.set(action.message_id, [...(byMessage.get(action.message_id) ?? []), action]);
  }

  return (
    <div className="order assistant">
      <section className="panel order-head">
        <div className="order-head__top">
          <h2>Assistant</h2>
          <div className="row order-head__actions">
            <button className="ghost" onClick={() => setNotesOpen((v) => !v)}>
              Notes
            </button>
          </div>
        </div>
      </section>

      <div className="order__split">
        <section className="panel panel--phase order__body assistant__body">
          <div className="chat" ref={chatRef}>
            {conversation.messages.length === 0 && (
              <p className="muted assistant__empty">
                Ask about the project, what to build next, or have it draft work orders. It
                proposes; nothing happens until you approve.
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
                placeholder="Ask the assistant…"
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key !== "Enter" || e.shiftKey) return;
                  e.preventDefault();
                  if (!running && !sending && draft.trim()) void send();
                }}
                disabled={sending}
              />
              <button onClick={() => void send()} disabled={running || sending || !draft.trim()}>
                {sending ? "Sending…" : "Send"}
              </button>
            </div>
          </div>
        </section>

        {notesOpen && (
          <Notes
            scope={scope}
            notes={conversation.notes}
            onSaved={setConversation}
            onClose={() => setNotesOpen(false)}
          />
        )}
      </div>
    </div>
  );
}

function ActionCard({
  scope,
  action,
  onChanged,
  onOpenOrder,
}: {
  scope: string;
  action: AssistantAction;
  onChanged: (conversation: Conversation) => void;
  onOpenOrder?: (featureId: string) => void;
}) {
  const modelName = useModelName();
  const agents = useAgents();
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState<null | "approve" | "start" | "dismiss">(null);
  const [error, setError] = useState<string | null>(null);
  const premium = action.premium ?? [];
  const pending = action.status === "proposed";

  const run = async (kind: NonNullable<typeof busy>, fn: () => Promise<Conversation>) => {
    setBusy(kind);
    setError(null);
    try {
      onChanged(await fn());
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
    }
  };
  const approve = (start: boolean) =>
    run(start ? "start" : "approve", () =>
      approveAssistantAction(scope, action.id, { start, confirm_premium: confirmed }),
    );

  const heading =
    action.type === "create_work_order"
      ? `New work order: ${action.title}`
      : action.type === "start_work_order"
        ? "Start a work order"
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
      <ul className="assistant-action__facts">
        {action.steps && <li>{STEPS_LABEL[action.steps]}</li>}
        {models.map((line) => (
          <li key={line as string}>{line}</li>
        ))}
        {action.independent_tests && <li>Independent acceptance tests</li>}
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
              {busy === "approve"
                ? "Applying…"
                : action.type === "start_work_order"
                  ? "Start"
                  : "Switch"}
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
  onClose,
}: {
  scope: string;
  notes: string;
  onSaved: (conversation: Conversation) => void;
  onClose: () => void;
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
    <aside className="panel order-side">
      <div className="order-side__head">
        <div className="order-side__tabs">
          <span className="order-side__tab order-side__tab--on">Notes</span>
        </div>
        <button className="order-side__chevron" onClick={onClose} aria-label="Close notes">
          ›
        </button>
      </div>
      <div className="order-side__body">
        <p className="muted note">
          What the assistant keeps for itself: decisions, your preferences, and reusable work. You
          can edit them.
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
    </aside>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === "assistant_unavailable") return "The assistant is unavailable: " + e.message;
    return e.message;
  }
  return String(e);
}
