import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  acceptBrief,
  getAssistant,
  reopenWorkOrder,
  replyToAssistant,
  startAssistant,
  updateHandoffBrief,
} from "../api/features";
import { startRun } from "../api/runs";
import { ApiError } from "../api/client";
import { Transcript, type TranscriptEntry } from "./Transcript";
import { useFeatureArtifact } from "./useFeatureArtifact";
import { WORK } from "../vocab";
import type { WorkOrderAssistantSession, Feature, HandoffBriefDocument } from "../api/types";

const POLL_MS = 1500;

// The first phase of a work order (ADR-015). A Draft is clarified with the
// project assistant into a handoff brief; a Ready order shows that brief and
// starts the agents; afterwards the brief stays visible as the order's basis.
export function ClarifyView({
  projectId,
  feature,
  hasRepo,
  onChanged,
}: {
  projectId: string;
  feature: Feature;
  hasRepo?: boolean;
  onChanged: () => void;
}) {
  const { artifact: brief } = useFeatureArtifact<HandoffBriefDocument>(
    projectId,
    feature.id,
    "handoff_brief",
  );

  if (feature.state === "draft") {
    return (
      <AssistantChat
        projectId={projectId}
        feature={feature}
        hasRepo={hasRepo}
        brief={brief?.document ?? null}
        briefRevision={brief?.revision ?? 0}
        onChanged={onChanged}
      />
    );
  }
  if (feature.state === "ready") {
    return (
      <ReadyView
        projectId={projectId}
        feature={feature}
        brief={brief?.document ?? null}
        briefRevision={brief?.revision ?? 0}
        onChanged={onChanged}
      />
    );
  }
  return (
    <section className="panel panel--phase">
      <h2>Handoff brief</h2>
      {brief ? (
        <BriefCard brief={brief.document} />
      ) : (
        <p>{feature.accepted_goal || "This work order has no handoff brief."}</p>
      )}
      <p className="muted note">Planning is the next phase — open it in the timeline above.</p>
    </section>
  );
}

function AssistantChat({
  projectId,
  feature,
  hasRepo,
  brief,
  briefRevision,
  onChanged,
}: {
  projectId: string;
  feature: Feature;
  hasRepo?: boolean;
  brief: HandoffBriefDocument | null;
  briefRevision: number;
  onChanged: () => void;
}) {
  const [session, setSession] = useState<WorkOrderAssistantSession | null>(null);
  const [reply, setReply] = useState("");
  const [busy, setBusy] = useState<null | "send" | "accept">(null);
  const [error, setError] = useState<string | null>(null);
  const chatRef = useRef<HTMLDivElement | null>(null);
  const pinnedToBottom = useRef(true);

  // Open the conversation as soon as the draft is shown: the work order's
  // title and description are the user's first message.
  const load = useCallback(async () => {
    try {
      setSession(await getAssistant(projectId, feature.id));
      setError(null);
    } catch (e) {
      if (e instanceof ApiError && e.code === "assistant_not_found") {
        if (hasRepo === false) return;
        try {
          setSession(await startAssistant(projectId, feature.id));
          setError(null);
        } catch (startError) {
          setError(describe(startError));
        }
        return;
      }
      setError(describe(e));
    }
  }, [projectId, feature.id, hasRepo]);

  useEffect(() => {
    void load();
  }, [load]);

  const running = session?.status === "running";
  useEffect(() => {
    if (!running) return;
    const id = setInterval(() => void load(), POLL_MS);
    return () => clearInterval(id);
  }, [running, load]);

  const entries = useMemo<TranscriptEntry[]>(
    () =>
      (session?.messages ?? []).map((m, i) => ({
        key: `${i}`,
        role: m.role,
        type: "message",
        text: m.text,
        at: m.occurred_at,
      })),
    [session],
  );

  useEffect(() => {
    const el = chatRef.current;
    if (el && pinnedToBottom.current) el.scrollTop = el.scrollHeight;
  }, [entries]);

  const send = async () => {
    const text = reply.trim();
    if (!text) return;
    setBusy("send");
    setError(null);
    try {
      setSession(await replyToAssistant(projectId, feature.id, text, crypto.randomUUID()));
      setReply("");
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
    }
  };

  const canReply = !!session && !running && busy === null;

  // Enter sends; Shift+Enter or Alt+Enter starts a new line.
  const onReplyKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== "Enter" || e.shiftKey) return;
    e.preventDefault();
    if (e.altKey) {
      const ta = e.currentTarget;
      const start = ta.selectionStart;
      setReply(reply.slice(0, start) + "\n" + reply.slice(ta.selectionEnd));
      requestAnimationFrame(() => {
        ta.selectionStart = ta.selectionEnd = start + 1;
      });
      return;
    }
    if (canReply && reply.trim()) void send();
  };

  const accept = async () => {
    setBusy("accept");
    setError(null);
    try {
      await acceptBrief(projectId, feature.id, crypto.randomUUID());
      onChanged();
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
    }
  };

  if (hasRepo === false && !session) {
    return (
      <section className="panel">
        <h2>Clarify</h2>
        <p className="muted">
          This project has no internal repository yet, so the assistant can't look at the code.
          Import a project or bind a repository first.
        </p>
      </section>
    );
  }

  return (
    <section className="panel panel--phase">
      <h2>Clarify with the assistant</h2>
      {error && <div className="banner banner--error">{error}</div>}
      <div className="clarify">
        <div className="clarify__main">
          <div
            className="chat"
            ref={chatRef}
            onScroll={(e) => {
              const el = e.currentTarget;
              pinnedToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
            }}
          >
            <Transcript entries={entries} empty="Starting the assistant…" />
          </div>
          <div className="composer">
            <p className="muted status-line">
              {!session
                ? "Starting…"
                : running
                  ? "The assistant is looking into it…"
                  : session.status === "failed"
                    ? "The assistant's last turn failed. Send a message to try again."
                    : "Your turn."}
            </p>
            <div className="reply">
              <textarea
                placeholder="Reply to the assistant…"
                value={reply}
                onChange={(e) => setReply(e.target.value)}
                onKeyDown={onReplyKeyDown}
                disabled={!canReply}
                rows={2}
              />
              <button onClick={() => void send()} disabled={!canReply || !reply.trim()}>
                {busy === "send" ? "Sending…" : "Send"}
              </button>
            </div>
          </div>
        </div>
        <aside className="clarify__side">
          {brief ? (
            <>
              <div className="clarify__scroll">
                <BriefEditor
                  projectId={projectId}
                  featureId={feature.id}
                  brief={brief}
                  revision={briefRevision}
                  disabled={running || busy !== null}
                />
              </div>
              <button
                className="primary"
                onClick={() => void accept()}
                disabled={running || busy !== null}
                title={`Makes this ${WORK.short} Ready; the agents start when you press Start.`}
              >
                {busy === "accept" ? "Accepting…" : "Accept brief"}
              </button>
            </>
          ) : (
            <p className="muted note">
              The assistant will propose a handoff brief here: the goal, the areas to touch, and
              what is worth planning around.
            </p>
          )}
        </aside>
      </div>
    </section>
  );
}

function ReadyView({
  projectId,
  feature,
  brief,
  briefRevision,
  onChanged,
}: {
  projectId: string;
  feature: Feature;
  brief: HandoffBriefDocument | null;
  briefRevision: number;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState<null | "start" | "reopen">(null);
  const [error, setError] = useState<string | null>(null);

  const run = async (action: "start" | "reopen") => {
    setBusy(action);
    setError(null);
    try {
      if (action === "start") {
        // One key per work order: a repeated click or retry joins the same run.
        await startRun(projectId, feature.id, `start-${feature.id}`);
      } else {
        await reopenWorkOrder(projectId, feature.id, crypto.randomUUID());
      }
      onChanged();
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <section className="panel panel--phase">
      <h2>Ready</h2>
      {error && <div className="banner banner--error">{error}</div>}
      <p className="muted">
        The lead and reviewer start from this brief. The lead first checks it against what has
        changed on main since it was written, then plans the commits with the reviewer.
      </p>
      {brief && (
        <BriefEditor
          projectId={projectId}
          featureId={feature.id}
          brief={brief}
          revision={briefRevision}
          disabled={busy !== null}
        />
      )}
      <div className="row">
        <button className="primary" onClick={() => void run("start")} disabled={busy !== null}>
          {busy === "start" ? "Starting…" : "Start"}
        </button>
        <button className="ghost" onClick={() => void run("reopen")} disabled={busy !== null}>
          {busy === "reopen" ? "Reopening…" : "Reopen for clarification"}
        </button>
      </div>
    </section>
  );
}

// The brief, shown read-only with an Edit toggle. Lists edit one item per line.
function BriefEditor({
  projectId,
  featureId,
  brief,
  revision,
  disabled,
}: {
  projectId: string;
  featureId: string;
  brief: HandoffBriefDocument;
  revision: number;
  disabled: boolean;
}) {
  const [editing, setEditing] = useState(false);
  const [goal, setGoal] = useState(brief.goal);
  const [areas, setAreas] = useState(brief.areas.join("\n"));
  const [considerations, setConsiderations] = useState(brief.considerations.join("\n"));
  const [questions, setQuestions] = useState(brief.open_questions.join("\n"));
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const startEditing = () => {
    setGoal(brief.goal);
    setAreas(brief.areas.join("\n"));
    setConsiderations(brief.considerations.join("\n"));
    setQuestions(brief.open_questions.join("\n"));
    setEditing(true);
  };

  const save = async () => {
    setSaving(true);
    setError(null);
    const lines = (text: string) =>
      text
        .split("\n")
        .map((line) => line.trim())
        .filter(Boolean);
    try {
      await updateHandoffBrief(
        projectId,
        featureId,
        revision,
        {
          ...brief,
          goal: goal.trim(),
          areas: lines(areas),
          considerations: lines(considerations),
          open_questions: lines(questions),
        },
        crypto.randomUUID(),
      );
      setEditing(false);
    } catch (e) {
      setError(
        e instanceof ApiError && e.code === "artifact_revision_conflict"
          ? "The brief changed while you were editing. Reopen the editor to see the latest."
          : describe(e),
      );
    } finally {
      setSaving(false);
    }
  };

  if (!editing) {
    return (
      <div className="brief">
        <BriefCard brief={brief} />
        <button className="linkish" onClick={startEditing} disabled={disabled}>
          Edit brief
        </button>
      </div>
    );
  }
  return (
    <div className="brief brief--editing">
      {error && <div className="banner banner--error">{error}</div>}
      <label className="accept__label">Goal</label>
      <textarea value={goal} onChange={(e) => setGoal(e.target.value)} rows={4} />
      <label className="accept__label">Areas to touch (one per line)</label>
      <textarea value={areas} onChange={(e) => setAreas(e.target.value)} rows={3} />
      <label className="accept__label">Worth planning around (one per line)</label>
      <textarea
        value={considerations}
        onChange={(e) => setConsiderations(e.target.value)}
        rows={3}
      />
      <label className="accept__label">Open questions (one per line)</label>
      <textarea value={questions} onChange={(e) => setQuestions(e.target.value)} rows={2} />
      <div className="row">
        <button className="primary" onClick={() => void save()} disabled={saving || !goal.trim()}>
          {saving ? "Saving…" : "Save brief"}
        </button>
        <button className="ghost" onClick={() => setEditing(false)} disabled={saving}>
          Cancel
        </button>
      </div>
    </div>
  );
}

function BriefCard({ brief }: { brief: HandoffBriefDocument }) {
  const section = (title: string, items: string[]) =>
    items.length > 0 && (
      <>
        <span className="accept__label">{title}</span>
        <ul>
          {items.map((item, i) => (
            <li key={i}>{item}</li>
          ))}
        </ul>
      </>
    );
  return (
    <div className="brief__card">
      <span className="accept__label">Goal</span>
      <p>{brief.goal}</p>
      {section("Areas to touch", brief.areas)}
      {section("Worth planning around", brief.considerations)}
      {section("Open questions", brief.open_questions)}
    </div>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}
