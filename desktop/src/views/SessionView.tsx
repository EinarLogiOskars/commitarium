import { useEffect, useMemo, useRef, useState } from "react";
import { deleteFeature, finishSession, renameFeature, sendSessionTurn } from "../api/features";
import { recoverRun } from "../api/runs";
import { ApiError } from "../api/client";
import { Transcript, type TranscriptEntry } from "./Transcript";
import { useSessionEvents } from "./useSessionEvents";
import { MergeView } from "./MergeView";
import { PREVIEW_STATE, primaryUrl, usePreview, type PreviewHandle } from "./usePreview";
import { openExternal } from "../ipc";
import { markSessionSeen } from "./sessionSeen";
import type { Feature, Run } from "../api/types";

const GREETING = "Hey, what can I help you with?";

// What belongs in the conversation: the session's plan record is bookkeeping,
// attempt_terminal repeats the agent's reply, and input_required shows in the
// status line.
const SHOWN = new Set(["message", "activity", "user_message"]);

// A session (ADR-019): one conversation with the lead agent on the session's
// own branch. The user directs every turn, previews the work, and finishes it
// through the merge gate, or discards it.
export function SessionView({
  projectId,
  feature,
  run,
  onChanged,
  onDeleted,
  onOpenValidation,
}: {
  projectId: string;
  feature: Feature;
  run: Run | null;
  onChanged: () => void;
  onDeleted?: () => void;
  onOpenValidation?: () => void;
}) {
  const [reply, setReply] = useState("");
  const [busy, setBusy] = useState<null | "send" | "finish" | "continue" | "rename" | "delete">(
    null,
  );
  const [error, setError] = useState<string | null>(null);
  const [renaming, setRenaming] = useState<string | null>(null);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const [forceDiscard, setForceDiscard] = useState(false);
  const chatRef = useRef<HTMLDivElement | null>(null);
  const pinnedToBottom = useRef(true);
  const preview = usePreview(projectId, feature.id);

  const lead = run?.sessions.find((s) => s.role === "lead");
  const sessionRefs = useMemo(() => (lead ? [{ id: lead.id, role: "lead" }] : []), [lead]);
  const { durable, previews } = useSessionEvents(sessionRefs, !!lead);

  // The conversation: the greeting, then messages and tool activity.
  const entries = useMemo<TranscriptEntry[]>(() => {
    const greeting: TranscriptEntry = {
      key: "greeting",
      role: "lead",
      type: "message",
      text: GREETING,
      at: run?.started_at ?? feature.created_at,
    };
    return [greeting, ...durable.filter((e) => SHOWN.has(e.type)), ...previews];
  }, [durable, previews, run?.started_at, feature.created_at]);

  useEffect(() => {
    const el = chatRef.current;
    if (el && pinnedToBottom.current) el.scrollTop = el.scrollHeight;
  }, [entries]);

  // An open session's latest reply is seen.
  useEffect(() => {
    if (run && run.status !== "running") markSessionSeen(feature.id, run.updated_at);
  }, [feature.id, run]);

  const done = feature.state === "completed" || feature.state === "cancelled";
  const working = run?.status === "running";
  const blocked = run?.status === "waiting_for_user" && run.wait_kind === "blocker";
  const atMergeGate = feature.state === "ready_to_merge";
  const canSend =
    !!run && !done && !working && !blocked && run.status === "waiting_for_user" && busy === null;
  const agentReplied = durable.some((e) => e.role === "lead" && e.type === "message");
  const canFinish = canSend && !atMergeGate && feature.state === "implementing" && agentReplied;

  const act = async (kind: NonNullable<typeof busy>, fn: () => Promise<unknown>) => {
    setBusy(kind);
    setError(null);
    try {
      await fn();
      onChanged();
      return true;
    } catch (e) {
      setError(describe(e));
      return false;
    } finally {
      setBusy(null);
    }
  };

  const send = async () => {
    const text = reply.trim();
    if (!text) return;
    pinnedToBottom.current = true;
    const sent = await act("send", () =>
      sendSessionTurn(projectId, feature.id, text, crypto.randomUUID()),
    );
    if (sent) setReply("");
  };

  // Enter sends; Shift+Enter starts a new line.
  const onKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key !== "Enter" || e.shiftKey) return;
    e.preventDefault();
    if (canSend && reply.trim()) void send();
  };

  const discard = async () => {
    setBusy("delete");
    setError(null);
    try {
      await preview.stop();
      await deleteFeature(projectId, feature.id, crypto.randomUUID(), forceDiscard);
      onDeleted?.();
    } catch (e) {
      if (e instanceof ApiError && e.code === "feature_active") {
        setForceDiscard(true);
        setError("The agent is still working. Discard again to stop it first.");
      } else {
        setError(describe(e));
      }
    } finally {
      setBusy(null);
    }
  };

  const status = !run
    ? "Starting…"
    : done
      ? feature.state === "completed"
        ? "Merged."
        : "Closed."
      : working
        ? run.reason || "The agent is working…"
        : blocked
          ? null
          : atMergeGate
            ? "Waiting at the merge gate. Send a message to keep working instead."
            : "Your turn.";

  return (
    <div className="order session">
      <section className="panel order-head">
        <div className="panel__head">
          <div className="session__title">
            {renaming === null ? (
              <h2>
                {feature.title}
                {!done && (
                  <button
                    className="linkish session__rename"
                    onClick={() => setRenaming(feature.title)}
                    disabled={busy !== null}
                  >
                    Rename
                  </button>
                )}
              </h2>
            ) : (
              <form
                className="row"
                onSubmit={(e) => {
                  e.preventDefault();
                  const title = renaming.trim();
                  if (!title) return;
                  void act("rename", () => renameFeature(projectId, feature.id, title)).then(
                    (ok) => ok && setRenaming(null),
                  );
                }}
              >
                <input
                  type="text"
                  value={renaming}
                  onChange={(e) => setRenaming(e.target.value)}
                  autoFocus
                  disabled={busy !== null}
                />
                <button className="primary" type="submit" disabled={busy !== null}>
                  Save
                </button>
                <button
                  className="ghost"
                  type="button"
                  onClick={() => setRenaming(null)}
                  disabled={busy !== null}
                >
                  Cancel
                </button>
              </form>
            )}
            <p className="muted session__kind">Session</p>
          </div>
          <div className="row">
            {!done && <SessionPreview preview={preview} />}
            {canFinish && (
              <button
                className="primary"
                onClick={() =>
                  void act("finish", () =>
                    finishSession(projectId, feature.id, crypto.randomUUID()),
                  )
                }
                title="The agent commits the work and the session goes to the merge gate."
              >
                {busy === "finish" ? "Finishing…" : "Finish"}
              </button>
            )}
            {onDeleted && feature.state !== "completed" && !confirmDiscard && (
              <button
                className="ghost danger"
                onClick={() => setConfirmDiscard(true)}
                disabled={busy !== null}
              >
                Discard
              </button>
            )}
          </div>
        </div>
        {confirmDiscard && (
          <div className="delete-confirm">
            <span className="muted">
              {forceDiscard
                ? "Stop the agent and discard this session? Its branch, pull request, and changes are removed."
                : "Discard this session? Its branch, pull request, and changes are removed; nothing reaches the default branch."}
            </span>
            <div className="row">
              <button className="danger" onClick={() => void discard()} disabled={busy !== null}>
                {busy === "delete" ? "Discarding…" : "Discard"}
              </button>
              <button
                className="ghost"
                onClick={() => {
                  setConfirmDiscard(false);
                  setForceDiscard(false);
                }}
                disabled={busy !== null}
              >
                Cancel
              </button>
            </div>
          </div>
        )}
        {error && <div className="banner banner--error">{error}</div>}
        {(preview.error || preview.status?.state === "failed") && !done && (
          <div className="banner banner--warn session__preview-error">
            Preview: {preview.error ?? preview.status?.error ?? "it stopped."}
          </div>
        )}
        {blocked && run && (
          <div className="banner banner--warn session__blocked">
            <span>{run.reason || "The session stopped."}</span>
            <button
              onClick={() =>
                void act("continue", () =>
                  recoverRun(run.id, crypto.randomUUID(), { action: "continue" }),
                )
              }
              disabled={busy !== null}
            >
              {busy === "continue" ? "Continuing…" : "Continue"}
            </button>
          </div>
        )}
      </section>

      <section className="panel panel--phase session__body">
        <div
          className="chat"
          ref={chatRef}
          onScroll={(e) => {
            const el = e.currentTarget;
            pinnedToBottom.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
          }}
        >
          <Transcript entries={entries} />
        </div>
        {(atMergeGate || done) && run && (
          <MergeView
            projectId={projectId}
            featureId={feature.id}
            runId={run.id}
            state={feature.state}
            live={!done}
            onAdvanced={onChanged}
            onOpenValidation={onOpenValidation}
          />
        )}
        {!done && (
          <div className="composer">
            {status && <p className="muted status-line">{status}</p>}
            <div className="reply">
              <textarea
                placeholder="Message the agent…"
                value={reply}
                onChange={(e) => setReply(e.target.value)}
                onKeyDown={onKeyDown}
                disabled={!canSend}
                rows={2}
              />
              <button onClick={() => void send()} disabled={!canSend || !reply.trim()}>
                {busy === "send" ? "Sending…" : "Send"}
              </button>
            </div>
          </div>
        )}
      </section>
    </div>
  );
}

// The session preview runs the project's stack from the session's working
// copy, so it shows changes before they are committed (ADR-019).
function SessionPreview({ preview }: { preview: PreviewHandle }) {
  const state = preview.status?.state ?? "stopped";
  const url = primaryUrl(preview.status);
  if (state === "starting") {
    return (
      <span className="row session__preview">
        <span className="muted">{PREVIEW_STATE.starting.label}</span>
        <button className="ghost" onClick={() => void preview.stop()}>
          Stop preview
        </button>
      </span>
    );
  }
  if (state === "running") {
    return (
      <span className="row session__preview">
        {url && <button onClick={() => void openExternal(url)}>Open preview</button>}
        <button
          className="ghost"
          onClick={() => void preview.start()}
          title="Rebuild the preview from the working copy"
        >
          Restart
        </button>
        <button className="ghost" onClick={() => void preview.stop()}>
          Stop
        </button>
      </span>
    );
  }
  return (
    <button
      onClick={() => void preview.start()}
      title="Run the project from this session's working copy, uncommitted changes included"
    >
      Preview
    </button>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === "session_busy") return "The agent is still working on your last message.";
    return e.message;
  }
  return String(e);
}
