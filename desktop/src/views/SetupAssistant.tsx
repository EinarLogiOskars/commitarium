import { useEffect, useRef, useState } from "react";
import {
  applyAssistantSession,
  getAssistantSession,
  sendAssistantMessage,
  startAssistantSession,
} from "../api/toolchains";
import { ApiError } from "../api/client";
import { useModels, pickModel } from "./useModels";
import { Markdown } from "./Markdown";
import type { AgentProvider, AssistantSession, ProjectToolchain } from "../api/types";

const POLL_MS = 2000;

/** "Help me choose" — a bounded provider conversation that ends in an exact
 * toolchain proposal the user applies. The assistant never touches the repo. */
export function SetupAssistant({
  projectId,
  onApplied,
  onCancel,
}: {
  projectId: string;
  onApplied: (t: ProjectToolchain) => void;
  onCancel: () => void;
}) {
  const { modelsFor, loading: modelsLoading } = useModels();
  const [provider, setProvider] = useState<AgentProvider>("claude");
  const [model, setModel] = useState("");
  const [description, setDescription] = useState("");
  const [session, setSession] = useState<AssistantSession | null>(null);
  const [reply, setReply] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const chatRef = useRef<HTMLDivElement | null>(null);

  // Default the model to a valid choice for the chosen provider's lead catalog.
  useEffect(() => {
    if (modelsLoading) return;
    setModel((m) => pickModel(modelsFor(provider, "lead"), m));
  }, [modelsLoading, modelsFor, provider]);

  // Poll while the assistant's turn is running.
  useEffect(() => {
    if (session?.status !== "running") return;
    const sid = session.id;
    const t = setInterval(async () => {
      try {
        setSession(await getAssistantSession(projectId, sid));
      } catch (e) {
        setError(describe(e));
      }
    }, POLL_MS);
    return () => clearInterval(t);
  }, [session?.status, session?.id, projectId]);

  useEffect(() => {
    const el = chatRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [session?.messages.length]);

  const start = async () => {
    if (!description.trim() || !model) return;
    setBusy(true);
    setError(null);
    try {
      setSession(
        await startAssistantSession(
          projectId,
          { provider, model, message: description.trim() },
          crypto.randomUUID(),
        ),
      );
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  const send = async () => {
    if (!reply.trim() || !session) return;
    setBusy(true);
    setError(null);
    try {
      const next = await sendAssistantMessage(projectId, session.id, reply.trim(), crypto.randomUUID());
      setReply("");
      setSession(next);
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  const apply = async () => {
    if (!session) return;
    setBusy(true);
    setError(null);
    try {
      onApplied(await applyAssistantSession(projectId, session.id, crypto.randomUUID()));
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  // Start form — before a session exists.
  if (!session) {
    const leadModels = modelsFor(provider, "lead");
    return (
      <div className="assistant">
        {error && <div className="banner banner--error">{error}</div>}
        <p className="muted">
          Describe what you want to build. An agent will ask a few questions and propose an exact
          runtime stack — it won't touch your repository or run anything.
        </p>
        <div className="assistant__setup">
          <label>
            Provider
            <select value={provider} onChange={(e) => setProvider(e.target.value as AgentProvider)} disabled={busy}>
              <option value="claude">Claude</option>
              <option value="codex">Codex</option>
            </select>
          </label>
          <label>
            Model
            <select value={model} onChange={(e) => setModel(e.target.value)} disabled={busy || leadModels.length === 0}>
              {leadModels.length === 0 ? (
                <option value="">No models available</option>
              ) : (
                leadModels.map((m) => (
                  <option key={m.id} value={m.id}>
                    {m.display_name}
                  </option>
                ))
              )}
            </select>
          </label>
        </div>
        <textarea
          placeholder="e.g. A small personal web app with a simple deployment story."
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          disabled={busy}
          rows={3}
        />
        <div className="assistant__actions">
          <button className="ghost" onClick={onCancel} disabled={busy}>
            Back to picker
          </button>
          <button className="primary" onClick={() => void start()} disabled={busy || !description.trim() || !model}>
            {busy ? "Starting…" : "Start"}
          </button>
        </div>
      </div>
    );
  }

  const { status, proposal } = session;
  return (
    <div className="assistant">
      {error && <div className="banner banner--error">{error}</div>}

      <div className="assistant__chat" ref={chatRef}>
        {session.messages.map((m, i) => (
          <div key={i} className={`msg ${m.role === "user" ? "msg--user" : "msg--lead"}`}>
            <span className="msg__who">{m.role === "user" ? "You" : "Assistant"}</span>
            <div className="msg__text">
              <Markdown text={m.text} />
            </div>
          </div>
        ))}
        {status === "running" && <p className="muted">Assistant is thinking…</p>}
      </div>

      {status === "proposal_ready" && proposal && (
        <div className="assistant__proposal">
          <h3>Proposed stack</h3>
          <div className="stack__chips">
            {Object.entries(proposal.tools).map(([t, v]) => (
              <span key={t} className="pill pill--ok">
                {t} {v}
              </span>
            ))}
          </div>
          {proposal.services.length > 0 && (
            <p className="muted note">
              External services (requirements only, not provisioned): {proposal.services.join(", ")}
            </p>
          )}
          <div className="assistant__actions">
            <button className="ghost" onClick={onCancel} disabled={busy}>
              Back to picker
            </button>
            <button className="primary" onClick={() => void apply()} disabled={busy}>
              {busy ? "Applying…" : "Use this stack"}
            </button>
          </div>
        </div>
      )}

      {status === "waiting_for_user" && (
        <div className="assistant__reply">
          <textarea
            placeholder="Your answer…"
            value={reply}
            onChange={(e) => setReply(e.target.value)}
            disabled={busy}
            rows={2}
          />
          <button className="primary" onClick={() => void send()} disabled={busy || !reply.trim()}>
            {busy ? "Sending…" : "Send"}
          </button>
        </div>
      )}

      {status === "failed" && (
        <div className="assistant__actions">
          <button className="primary" onClick={onCancel} disabled={busy}>
            Back to picker
          </button>
        </div>
      )}
    </div>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}
