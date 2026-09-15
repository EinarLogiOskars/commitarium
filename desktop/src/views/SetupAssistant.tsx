import { useEffect, useMemo, useRef, useState } from "react";
import {
  applyAssistantSession,
  getAssistantSession,
  sendAssistantMessage,
  startAssistantSession,
} from "../api/toolchains";
import { ApiError } from "../api/client";
import { useModels, pickModel } from "./useModels";
import { Markdown } from "./Markdown";
import type {
  AgentProvider,
  AssistantPurpose,
  AssistantSession,
  ProjectToolchain,
} from "../api/types";

const POLL_MS = 2000;
const PROVIDERS: AgentProvider[] = ["claude", "codex"];
const LABEL: Record<AgentProvider, string> = { claude: "Claude", codex: "Codex" };

/** "Ask an agent" — a bounded provider conversation that ends in an exact
 * toolchain proposal the user applies. Two purposes: design_stack (describe a
 * new project) and verify_repository (agent inspects an imported repo). Never
 * touches the repository. The provider/model is pinned once a session starts. */
export function SetupAssistant({
  projectId,
  purpose = "design_stack",
  preferred,
  onApplied,
  onCancel,
}: {
  projectId: string;
  purpose?: AssistantPurpose;
  /** Project's configured lead provider/model — the first choice when connected. */
  preferred?: { provider?: AgentProvider; model?: string };
  onApplied: (t: ProjectToolchain) => void;
  onCancel: () => void;
}) {
  const { modelsFor, loading: modelsLoading } = useModels();
  const verify = purpose === "verify_repository";

  // A provider is usable only if its lead catalog has models (i.e. connected).
  const available = useMemo(
    () => PROVIDERS.filter((p) => modelsFor(p, "lead").length > 0),
    [modelsFor],
  );

  const [provider, setProvider] = useState<AgentProvider | null>(null);
  const [model, setModel] = useState("");
  const [description, setDescription] = useState("");
  const [session, setSession] = useState<AssistantSession | null>(null);
  const [reply, setReply] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const chatRef = useRef<HTMLDivElement | null>(null);

  // Selection order: preferred (project lead) if connected, else first connected.
  useEffect(() => {
    if (modelsLoading || session) return;
    setProvider((cur) => {
      if (cur && available.includes(cur)) return cur;
      if (preferred?.provider && available.includes(preferred.provider)) return preferred.provider;
      return available[0] ?? null;
    });
  }, [modelsLoading, available, preferred?.provider, session]);

  // Keep the model valid for the chosen provider; seed from preferred when it fits.
  useEffect(() => {
    if (modelsLoading || !provider || session) return;
    setModel((m) => pickModel(modelsFor(provider, "lead"), m || preferred?.model || ""));
  }, [modelsLoading, provider, modelsFor, preferred?.model, session]);

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
    if (!provider || !model) return;
    if (!verify && !description.trim()) return;
    setBusy(true);
    setError(null);
    try {
      setSession(
        await startAssistantSession(
          projectId,
          {
            provider,
            model,
            purpose,
            ...(verify ? {} : { message: description.trim() }),
          },
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

  // Enter submits; Alt+Enter (or Shift+Enter) inserts a newline. Alt+Enter has no
  // default newline in a textarea, so insert it by hand and sync React state.
  const submitOnEnter =
    (submit: () => void, set: (v: string) => void) =>
    (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
      if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
      if (e.shiftKey) return; // default newline
      if (e.altKey) {
        e.preventDefault();
        const el = e.currentTarget;
        el.setRangeText("\n", el.selectionStart, el.selectionEnd, "end");
        set(el.value);
        return;
      }
      e.preventDefault();
      if (!busy) submit();
    };

  const apply = async () => {
    if (!session) return;
    setBusy(true);
    setError(null);
    try {
      onApplied(await applyAssistantSession(projectId, session.id, crypto.randomUUID()));
    } catch (e) {
      // The repo moved under a verification — the pinned evidence is stale, so
      // the proposal can't be trusted. Drop the session and let them re-verify.
      if (e instanceof ApiError && e.code === "toolchain_assistant_stale") {
        setSession(null);
        setError("The repository changed since this check. Start the verification again.");
      } else {
        setError(describe(e));
      }
    } finally {
      setBusy(false);
    }
  };

  // No connected provider — agent assistance isn't possible.
  if (!modelsLoading && available.length === 0 && !session) {
    return (
      <div className="assistant">
        {error && <div className="banner banner--error">{error}</div>}
        <p>
          No agent provider is connected, so an agent can't {verify ? "verify this repository" : "help choose a stack"} yet.
          Connect Codex or Claude under Providers, or choose a stack manually.
        </p>
        <div className="assistant__actions">
          <button className="primary" onClick={onCancel} disabled={busy}>
            Choose a stack manually
          </button>
        </div>
      </div>
    );
  }

  // Start form — before a session exists.
  if (!session) {
    const leadModels = provider ? modelsFor(provider, "lead") : [];
    return (
      <div className="assistant">
        {error && <div className="banner banner--error">{error}</div>}
        <p className="muted">
          {verify
            ? "An agent reads this repository's committed files and proposes an exact runtime stack, explaining what it found. It won't modify or run anything."
            : "Describe what you want to build. An agent asks a few questions and proposes an exact runtime stack — it won't touch your repository or run anything."}{" "}
          This uses one provider turn.
        </p>
        <div className="assistant__setup">
          <label>
            Agent
            <select
              value={provider ?? ""}
              onChange={(e) => setProvider(e.target.value as AgentProvider)}
              disabled={busy}
            >
              {available.map((p) => (
                <option key={p} value={p}>
                  {LABEL[p]}
                </option>
              ))}
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
        {!verify && (
          <textarea
            placeholder="e.g. A small personal web app with a simple deployment story."
            value={description}
            onChange={(e) => setDescription(e.target.value)}
            onKeyDown={submitOnEnter(() => void start(), setDescription)}
            disabled={busy}
            rows={3}
          />
        )}
        {!verify && <p className="muted note">Enter to send · Alt+Enter for a new line</p>}
        <div className="assistant__actions">
          <button className="ghost" onClick={onCancel} disabled={busy}>
            Back to picker
          </button>
          <button
            className="primary"
            onClick={() => void start()}
            disabled={busy || !provider || !model || (!verify && !description.trim())}
          >
            {busy ? "Starting…" : verify ? "Verify repository" : "Start"}
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
            placeholder="Your answer… (Enter to send · Alt+Enter for a new line)"
            value={reply}
            onChange={(e) => setReply(e.target.value)}
            onKeyDown={submitOnEnter(() => void send(), setReply)}
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
