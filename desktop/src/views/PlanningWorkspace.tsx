import { useCallback, useEffect, useRef, useState } from "react";
import { finishPlanning, getVision } from "../api/vision";
import {
  addDocument,
  listDocuments,
  previewDocument,
  removeDocument,
  MAX_DOCUMENT_BYTES,
  type ProjectDocument,
} from "../api/documents";
import { ApiError } from "../api/client";
import { ASSISTANT_CHANGED } from "./AssistantDrawer";
import { openAssistant } from "../attention/askAssistant";
import { Markdown } from "./Markdown";
import { openExternal } from "../ipc";
import { Overlay } from "./Overlay";
import type { Vision, VisionItem, VisionPlanning } from "../api/types";

const POLL_MS = 3000;
// Decisions and questions longer than this show cut short, and open in full.
const LONG_LINE = 140;

// What is open in the detail overlay.
type Detail =
  | { kind: "line"; title: string; text: string }
  | { kind: "research"; note: VisionPlanning["research"][number] }
  | { kind: "item"; label: string; item: VisionItem }
  | { kind: "document"; name: string };

// The planning workspace (planning mode, Developer mode): the assistant's
// chat, in its drawer beside the page, and the plan it fills in as the user
// and it talk: the goal, decisions, open questions, research with sources,
// documents, the stack, and the build plan. The plan is the vision being
// written; Build it finishes it and hands it to autopilot, or to the user.
export function PlanningWorkspace({
  projectId,
  vision,
  onChanged,
}: {
  projectId: string;
  vision: Vision;
  /** The vision changed: refreshed, or built. */
  onChanged: (vision: Vision) => void;
}) {
  const [docs, setDocs] = useState<ProjectDocument[]>([]);
  const [busy, setBusy] = useState<null | "build" | "self" | "upload" | string>(null);
  const [error, setError] = useState<string | null>(null);
  const [detail, setDetail] = useState<Detail | null>(null);
  const closeDetail = useCallback(() => setDetail(null), []);
  const fileRef = useRef<HTMLInputElement | null>(null);

  const loadDocs = useCallback(() => {
    listDocuments(projectId)
      .then((r) => setDocs(r.documents))
      .catch(() => setDocs([]));
  }, [projectId]);

  // The plan fills in while the user and the assistant talk.
  useEffect(() => {
    loadDocs();
    const timer = setInterval(() => {
      getVision(projectId)
        .then(onChanged)
        .catch(() => undefined);
    }, POLL_MS);
    return () => clearInterval(timer);
  }, [projectId, onChanged, loadDocs]);

  // The conversation is in the assistant's drawer, beside the plan.
  useEffect(() => openAssistant(), [projectId]);

  const upload = async (files: FileList | null) => {
    if (!files?.length) return;
    setBusy("upload");
    setError(null);
    try {
      for (const file of Array.from(files)) {
        if (file.size > MAX_DOCUMENT_BYTES) {
          throw new Error(`${file.name} is larger than 5 MB.`);
        }
        await addDocument(projectId, file);
      }
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
      if (fileRef.current) fileRef.current.value = "";
      loadDocs();
    }
  };

  const remove = async (name: string) => {
    setBusy(name);
    setError(null);
    try {
      await removeDocument(projectId, name);
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
      loadDocs();
    }
  };

  const build = async (autopilot: boolean) => {
    setBusy(autopilot ? "build" : "self");
    setError(null);
    try {
      onChanged(await finishPlanning(projectId, autopilot));
      // How the assistant works here, and its launch, may have changed.
      window.dispatchEvent(new Event(ASSISTANT_CHANGED));
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
    }
  };

  const plan = vision.plan;
  const planning = vision.planning;
  const items = [...plan.priorities]
    .sort((a, b) => a.level - b.level)
    .filter((priority) => priority.items.length > 0);

  return (
    <div className="vision planning">
      <div className="vision__head">
        <h2>Planning</h2>
        <button className="ghost" onClick={openAssistant}>
          Talk to the assistant
        </button>
      </div>
      <p className="muted note">
        Plan it with the assistant: the plan fills in as you talk. Nothing is built until you say
        so.
      </p>
      {error && <div className="banner banner--error">{error}</div>}

      <section className="panel vision__statement">
        <h3>Goal</h3>
        <Markdown text={plan.statement} />
      </section>

      <div className="planning__grid">
        <PlanCard title="Decisions" empty="Nothing decided yet.">
          {planning?.decisions.map((d) => (
            <PlanLine
              key={d}
              text={d}
              onOpen={() => setDetail({ kind: "line", title: "Decision", text: d })}
            />
          ))}
        </PlanCard>
        <PlanCard title="Open questions" empty="No open questions.">
          {planning?.open_questions.map((q) => (
            <PlanLine
              key={q}
              text={q}
              onOpen={() => setDetail({ kind: "line", title: "Open question", text: q })}
            />
          ))}
        </PlanCard>
      </div>

      <section className="panel">
        <h3>Research</h3>
        {(planning?.research ?? []).length === 0 ? (
          <p className="muted note">
            When something needs looking up, the assistant does it and the findings show here.
          </p>
        ) : (
          <ul className="planning__list">
            {planning?.research.map((note) => (
              <li key={`${note.title}-${note.added_at}`}>
                <button
                  className="planning__open"
                  onClick={() => setDetail({ kind: "research", note })}
                >
                  {note.title}
                </button>
                {note.sources.length > 0 && (
                  <span className="muted note">
                    {" "}
                    · {note.sources.length} {note.sources.length === 1 ? "source" : "sources"}
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="panel">
        <div className="vision__head">
          <h3>Documents</h3>
          <button
            className="ghost"
            onClick={() => fileRef.current?.click()}
            disabled={busy !== null}
          >
            {busy === "upload" ? "Adding…" : "Add a document"}
          </button>
          <input
            ref={fileRef}
            type="file"
            multiple
            hidden
            onChange={(e) => void upload(e.target.files)}
          />
        </div>
        {docs.length === 0 ? (
          <p className="muted note">
            Add specs, API docs, or sample files. The assistant and the agents can read them.
          </p>
        ) : (
          <ul className="planning__list">
            {docs.map((doc) => (
              <li key={doc.name} className="vision__row">
                <span className="vision__text">
                  <span>
                    <button
                      className="planning__open"
                      onClick={() => setDetail({ kind: "document", name: doc.name })}
                    >
                      {doc.name}
                    </button>{" "}
                    <span className="muted note">{size(doc.size)}</span>
                  </span>
                </span>
                <button
                  className="ghost vision__actions"
                  onClick={() => void remove(doc.name)}
                  disabled={busy !== null}
                >
                  {busy === doc.name ? "Removing…" : "Remove"}
                </button>
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="panel">
        <h3>Stack</h3>
        {plan.stack ? (
          <p className="note">
            {plan.stack.frameworks || plan.stack.presets.join(", ")}
            {plan.stack.frameworks && (
              <span className="muted"> · {plan.stack.presets.join(", ")}</span>
            )}
          </p>
        ) : (
          <p className="muted note">Not decided yet.</p>
        )}
      </section>

      <section className="panel">
        <h3>Build plan</h3>
        {items.length === 0 ? (
          <p className="muted note">The build plan fills in as you settle what to build.</p>
        ) : (
          items.map((priority) => (
            <div key={priority.level} className="planning__priority">
              <strong className="muted note">
                P{priority.level}
                {priority.level === 0 ? " · needed first" : ""}
              </strong>
              <PlanItems
                items={priority.items}
                prefix={`P${priority.level}`}
                onOpen={(label, item) => setDetail({ kind: "item", label, item })}
              />
            </div>
          ))
        )}
      </section>

      <div className="row planning__build">
        <button
          className="primary"
          onClick={() => void build(true)}
          disabled={busy !== null || items.length === 0}
          title={items.length === 0 ? "The build plan needs at least one item." : undefined}
        >
          {busy === "build" ? "Starting…" : "Build it"}
        </button>
        <button
          onClick={() => void build(false)}
          disabled={busy !== null || items.length === 0}
          title="Finish the plan and build it yourself, work order by work order."
        >
          {busy === "self" ? "Finishing…" : "I'll build it myself"}
        </button>
        <span className="muted note">
          {items.length === 0
            ? "The build plan needs at least one item first."
            : "Build it hands the plan to the assistant, which sets the project up and builds it."}
        </span>
      </div>

      {detail && (
        <Overlay
          title={
            detail.kind === "line"
              ? detail.title
              : detail.kind === "research"
                ? detail.note.title
                : detail.kind === "item"
                  ? `${detail.label} ${detail.item.title}`
                  : detail.name
          }
          onClose={closeDetail}
        >
          {detail.kind === "line" && <p className="overlay__text">{detail.text}</p>}
          {detail.kind === "research" && (
            <>
              <Markdown text={detail.note.findings} />
              {detail.note.sources.length > 0 && (
                <div>
                  <h3>Sources</h3>
                  <ul className="planning__list">
                    {detail.note.sources.map((source) => (
                      <li key={source}>
                        <button
                          className="planning__open planning__source"
                          onClick={() => void openExternal(source)}
                          title={source}
                        >
                          {source}
                        </button>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </>
          )}
          {detail.kind === "item" &&
            (detail.item.description ? (
              <Markdown text={detail.item.description} />
            ) : (
              <p className="muted">No description yet.</p>
            ))}
          {detail.kind === "document" && (
            <DocumentPreview projectId={projectId} name={detail.name} />
          )}
        </Overlay>
      )}
    </div>
  );
}

// A decision or question, cut short when long; a long one opens in full.
function PlanLine({ text, onOpen }: { text: string; onOpen: () => void }) {
  if (text.length <= LONG_LINE) return <li>{text}</li>;
  return (
    <li>
      <button className="planning__open" onClick={onOpen}>
        {text.slice(0, LONG_LINE).trimEnd()}…
      </button>
    </li>
  );
}

// The start of a text document; other files have no preview.
function DocumentPreview({ projectId, name }: { projectId: string; name: string }) {
  const [file, setFile] = useState<{ preview: string; truncated: boolean } | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    previewDocument(projectId, name)
      .then(setFile)
      .catch((e: unknown) => setError(describe(e)));
  }, [projectId, name]);

  if (error) return <div className="banner banner--error">{error}</div>;
  if (!file) return <p className="muted">Loading…</p>;
  if (!file.preview) return <p className="muted">There is no preview for this file.</p>;
  return (
    <>
      <pre className="overlay__preview">{file.preview}</pre>
      {file.truncated && <p className="muted note">Only the start of the file is shown.</p>}
    </>
  );
}

function PlanCard({
  title,
  empty,
  children,
}: {
  title: string;
  empty: string;
  children?: React.ReactNode[];
}) {
  return (
    <section className="panel">
      <h3>{title}</h3>
      {children && children.length > 0 ? (
        <ul className="planning__list">{children}</ul>
      ) : (
        <p className="muted note">{empty}</p>
      )}
    </section>
  );
}

// The build plan's items by title; each opens with its full description.
function PlanItems({
  items,
  prefix,
  onOpen,
}: {
  items: VisionItem[];
  prefix: string;
  onOpen: (label: string, item: VisionItem) => void;
}) {
  return (
    <ul className="vision__items">
      {items.map((item, index) => (
        <li key={item.id}>
          <div className="vision__row">
            <span className="vision__label">
              {prefix}.{index + 1}
            </span>
            <div className="vision__text">
              <button
                className="planning__open"
                onClick={() => onOpen(`${prefix}.${index + 1}`, item)}
              >
                <strong>{item.title}</strong>
              </button>
            </div>
          </div>
          {item.items && item.items.length > 0 && (
            <PlanItems items={item.items} prefix={`${prefix}.${index + 1}`} onOpen={onOpen} />
          )}
        </li>
      ))}
    </ul>
  );
}

function size(bytes: number): string {
  return bytes < 1024 * 1024
    ? `${Math.max(1, Math.round(bytes / 1024))} KB`
    : `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return e.message;
  return e instanceof Error ? e.message : String(e);
}
