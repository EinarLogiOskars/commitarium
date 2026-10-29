import { useCallback, useEffect, useRef, useState } from "react";
import { getConversation } from "../api/assistant";
import { ApiError } from "../api/client";
import {
  addDocument,
  listDocuments,
  MAX_DOCUMENT_BYTES,
  previewDocument,
  removeDocument,
  type ProjectDocument,
} from "../api/documents";
import type { ResearchNote, Vision, VisionPlanning } from "../api/types";
import { openExternal } from "../ipc";
import { DOCUMENTS_CHANGED } from "./Attachments";
import { ASSISTANT_CHANGED } from "./chatEvents";
import { Markdown } from "./Markdown";
import { groupByTopic, TOPICS, withoutTopic } from "./visionPlans";

// What the assistant works from while planning (the Plan card's Insights),
// and the pieces the vision page shows of what the project was planned
// from: decisions by topic, research with its sources, and the project's
// documents. They took over from the planning workspace page (ADR-039).

/** The checklist keys of a planning (vision.PlanningChecklist, or
 * RoundChecklist for a next round). */
const CHECKLIST = ["purpose", "features", "data", "look", "details", "priorities", "stack"];
const ROUND_CHECKLIST = ["round", "features", "data", "look", "details", "priorities"];

/** The assistant's working notes for the planning session in progress:
 * what the plan covers, the decisions so far, the open questions, research
 * with its sources (and what it is still looking up), and the documents. */
export function PlanInsights({ projectId, vision }: { projectId: string; vision: Vision }) {
  const planning = vision.planning;
  const researching = useResearching(projectId);
  if (!planning) return <p className="muted">Nothing is being planned.</p>;
  return (
    <div className="plan-insights">
      <section>
        <h3>What the plan covers</h3>
        <CoveredList planning={planning} />
      </section>
      <section>
        <h3>Decisions so far</h3>
        <DecisionGroups lines={planning.decisions} empty="Nothing decided yet." />
      </section>
      <section>
        <h3>Open questions</h3>
        {planning.open_questions.length === 0 ? (
          <p className="muted note">Nothing open.</p>
        ) : (
          <ul className="planning__list planning__list--open">
            {planning.open_questions.map((q) => (
              <li key={q}>{withoutTopic(q)}</li>
            ))}
          </ul>
        )}
      </section>
      <section>
        <h3>Research</h3>
        {researching.length > 0 && (
          <ul className="planning__list">
            {researching.map((title) => (
              <li key={title} className="assistant__applied--running">
                <span className="assistant__pulse" aria-hidden /> Looking up {title}…
              </li>
            ))}
          </ul>
        )}
        <ResearchList
          notes={planning.research}
          empty={
            researching.length > 0
              ? null
              : "When something needs looking up, the assistant does it and the findings show here."
          }
        />
      </section>
      <section>
        <h3>Documents</h3>
        <ProjectDocuments projectId={projectId} />
      </section>
    </div>
  );
}

/** The research the assistant has under way, followed while shown. */
function useResearching(projectId: string): string[] {
  const [researching, setResearching] = useState<string[]>([]);
  useEffect(() => {
    const load = () =>
      getConversation(projectId)
        .then((c) => setResearching(c.researching ?? []))
        .catch(() => undefined);
    load();
    const timer = setInterval(load, 5000);
    window.addEventListener(ASSISTANT_CHANGED, load);
    return () => {
      clearInterval(timer);
      window.removeEventListener(ASSISTANT_CHANGED, load);
    };
  }, [projectId]);
  return researching;
}

/** The planning's checklist, with what the plan covers ticked and the topic
 * the conversation is on marked. */
export function CoveredList({ planning }: { planning: VisionPlanning }) {
  const keys = planning.round ? ROUND_CHECKLIST : CHECKLIST;
  const covered = new Set(planning.covered ?? []);
  return (
    <ul className="plan-covered">
      {keys.map((key) => {
        const label = TOPICS.find((t) => t.key === key)?.label ?? key;
        const done = covered.has(key);
        return (
          <li
            key={key}
            className={`plan-covered__item ${done ? "plan-covered__item--done" : ""} ${
              planning.step === key ? "plan-covered__item--now" : ""
            }`}
            title={planning.step === key ? "The conversation is on this now" : undefined}
          >
            <span className="plan-covered__mark" aria-hidden>
              {done ? "✓" : ""}
            </span>
            {label}
          </li>
        );
      })}
    </ul>
  );
}

/** Decisions grouped by their topic. */
export function DecisionGroups({ lines, empty }: { lines: string[]; empty: string }) {
  const groups = groupByTopic(lines);
  if (groups.length === 0) return <p className="muted note">{empty}</p>;
  return (
    <>
      {groups.map((group) => (
        <div key={group.key} className="planning__topic">
          <strong className="muted note">{group.label}</strong>
          <ul className="planning__list planning__list--decided">
            {group.lines.map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ul>
        </div>
      ))}
    </>
  );
}

/** Research notes by title; each opens to its findings and sources. */
export function ResearchList({ notes, empty }: { notes: ResearchNote[]; empty: string | null }) {
  if (notes.length === 0) return empty ? <p className="muted note">{empty}</p> : null;
  return (
    <ul className="planning__list">
      {notes.map((note) => (
        <li key={`${note.title}-${note.added_at}`}>
          <details className="planning__fold">
            <summary>
              <strong>{note.title}</strong>
              {note.sources.length > 0 && (
                <span className="muted note">
                  {" "}
                  · {note.sources.length} {note.sources.length === 1 ? "source" : "sources"}
                </span>
              )}
            </summary>
            <Markdown text={note.findings} />
            {note.sources.length > 0 && (
              <ul className="planning__list">
                {note.sources.map((source) => (
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
            )}
          </details>
        </li>
      ))}
    </ul>
  );
}

/** The project's documents (specs, API docs, samples) the assistant and
 * agents read: each opens to the start of it; they can be added and
 * removed. */
export function ProjectDocuments({ projectId }: { projectId: string }) {
  const [docs, setDocs] = useState<ProjectDocument[] | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const fileRef = useRef<HTMLInputElement | null>(null);

  const load = useCallback(() => {
    listDocuments(projectId)
      .then((r) => setDocs(r.documents))
      .catch(() => setDocs([]));
  }, [projectId]);
  useEffect(() => {
    load();
    // Files attached in the conversation are documents too.
    window.addEventListener(DOCUMENTS_CHANGED, load);
    return () => window.removeEventListener(DOCUMENTS_CHANGED, load);
  }, [load]);

  const change = async (what: string, action: () => Promise<unknown>) => {
    setBusy(what);
    setError(null);
    try {
      await action();
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
      if (fileRef.current) fileRef.current.value = "";
      load();
    }
  };
  const upload = (files: FileList | null) => {
    if (!files?.length) return;
    void change("upload", async () => {
      for (const file of Array.from(files)) {
        if (file.size > MAX_DOCUMENT_BYTES) throw new Error(`${file.name} is larger than 5 MB.`);
        await addDocument(projectId, file);
      }
    });
  };

  return (
    <div className="plan-docs">
      {docs === null ? (
        <p className="muted note">Loading…</p>
      ) : docs.length === 0 ? (
        <p className="muted note">
          Add specs, API docs, or sample files. The assistant and the agents can read them.
        </p>
      ) : (
        <ul className="planning__list">
          {docs.map((doc) => (
            <li key={doc.name}>
              <details className="planning__fold">
                <summary>
                  <strong>{doc.name}</strong> <span className="muted note">{size(doc.size)}</span>
                  <button
                    className="shell-card__link plan-docs__remove"
                    onClick={(e) => {
                      e.preventDefault();
                      void change(doc.name, () => removeDocument(projectId, doc.name));
                    }}
                    disabled={busy !== null}
                  >
                    {busy === doc.name ? "Removing…" : "Remove"}
                  </button>
                </summary>
                <DocumentPreview projectId={projectId} name={doc.name} />
              </details>
            </li>
          ))}
        </ul>
      )}
      <button
        className="shell-card__link"
        onClick={() => fileRef.current?.click()}
        disabled={busy !== null}
      >
        {busy === "upload" ? "Adding…" : "Add a document"}
      </button>
      <input ref={fileRef} type="file" multiple hidden onChange={(e) => upload(e.target.files)} />
      {error && <div className="banner banner--error">{error}</div>}
    </div>
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
  if (!file) return <p className="muted note">Loading…</p>;
  if (!file.preview) return <p className="muted note">There is no preview for this file.</p>;
  return (
    <>
      <pre className="overlay__preview">{file.preview}</pre>
      {file.truncated && <p className="muted note">Only the start of the file is shown.</p>}
    </>
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
