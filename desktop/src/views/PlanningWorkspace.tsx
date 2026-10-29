import { useCallback, useEffect, useRef, useState } from "react";
import { finishPlanning, getVision } from "../api/vision";
import { getConversation } from "../api/assistant";
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
import { DOCUMENTS_CHANGED } from "./Attachments";
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
  // The step the user is looking at; null follows the conversation.
  const [viewed, setViewed] = useState<number | null>(null);
  // Research the assistant is still looking up, shown while it runs.
  const [researching, setResearching] = useState<string[]>([]);
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
      getConversation(projectId)
        .then((c) => setResearching(c.researching ?? []))
        .catch(() => undefined);
    }, POLL_MS);
    return () => clearInterval(timer);
  }, [projectId, onChanged, loadDocs]);

  // Files attached in the conversation are documents too.
  useEffect(() => {
    window.addEventListener(DOCUMENTS_CHANGED, loadDocs);
    return () => window.removeEventListener(DOCUMENTS_CHANGED, loadDocs);
  }, [loadDocs]);

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
  const current = stepIndex(planning?.step);
  const decisions = sortByStep(planning?.decisions ?? []);
  const questions = sortByStep(planning?.open_questions ?? []);
  const step = STEPS[viewed ?? current];

  const openLine = (title: string, text: string) => setDetail({ kind: "line", title, text });
  const lines = (list: string[], title: string) =>
    list.map((text) => <PlanLine key={text} text={text} onOpen={() => openLine(title, text)} />);

  const goal = (
    <section className="panel vision__statement">
      <h3>Goal</h3>
      <Markdown text={plan.statement} />
    </section>
  );
  const research = (
    <section className="panel">
      <h3>Research</h3>
      {researching.length > 0 && (
        <ul className="planning__list planning__researching">
          {researching.map((title) => (
            <li key={title} className="assistant__applied--running">
              <span className="assistant__pulse" aria-hidden />
              Looking up {title}… You can keep answering the assistant meanwhile.
            </li>
          ))}
        </ul>
      )}
      {(planning?.research ?? []).length === 0 ? (
        researching.length > 0 ? null : (
          <p className="muted note">
            When something needs looking up, the assistant does it and the findings show here.
          </p>
        )
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
  );
  const documents = (
    <section className="panel">
      <div className="vision__head">
        <h3>Documents</h3>
        <button className="ghost" onClick={() => fileRef.current?.click()} disabled={busy !== null}>
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
  );
  const stack = (
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
  );
  const buildPlan = (
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
  );

  return (
    <div className="vision planning">
      {/* The title row, Build it, and the steps stay in reach while the plan
          scrolls. */}
      <div className="planning__head">
        <div className="planning__bar">
          <h2>Planning</h2>
          <span className="planning__bar-actions">
            <button className="ghost" onClick={() => openAssistant(true)}>
              Talk to the assistant
            </button>
            <button
              className="primary"
              onClick={() => void build(true)}
              disabled={busy !== null || items.length === 0}
              title={
                items.length === 0
                  ? "The build plan needs at least one item first."
                  : "Hand the plan to the assistant, which sets the project up and builds it."
              }
            >
              {busy === "build" ? "Starting…" : "Build it"}
            </button>
          </span>
        </div>
        {error && <div className="banner banner--error">{error}</div>}

        <nav className="stepper planning__steps" aria-label="Planning steps">
          {STEPS.map((s, i) => {
            // A step is done once the conversation has moved past it, or, after
            // going back to an earlier one, when it already has decisions.
            const state =
              i === current
                ? "active"
                : i < current || (decisions[s.key]?.length ?? 0) > 0
                  ? "done"
                  : "pending";
            const open = questions[s.key]?.length ?? 0;
            return (
              <div className="stepper__cell" key={s.key}>
                {i > 0 && <span className="stepper__line" aria-hidden />}
                <button
                  className={[
                    "stepper__step",
                    `stepper__step--${state}`,
                    s === step ? "stepper__step--viewed" : "",
                  ]
                    .filter(Boolean)
                    .join(" ")}
                  aria-current={s === step ? "step" : undefined}
                  title={s.hint + (open > 0 && s.key !== "review" ? ` · ${open} still open` : "")}
                  onClick={() => setViewed(i === current ? null : i)}
                >
                  <span className="stepper__marker">{state === "done" ? "✓" : i + 1}</span>
                  <span className="stepper__label">{s.label}</span>
                </button>
              </div>
            );
          })}
        </nav>
      </div>

      {step.key === "review" ? (
        <>
          <p className="muted note">
            Everything you and the assistant settled. Build it when it looks right, or ask the
            assistant to change anything.
          </p>
          {goal}
          <section className="panel">
            <h3>Decisions</h3>
            {(planning?.decisions ?? []).length === 0 ? (
              <p className="muted note">Nothing decided yet.</p>
            ) : (
              STEPS.filter((s) => decisions[s.key]?.length).map((s) => (
                <div key={s.key} className="planning__topic">
                  <strong className="muted note">{s.label}</strong>
                  <ul className="planning__list planning__list--decided">
                    {lines(decisions[s.key], "Decision")}
                  </ul>
                </div>
              ))
            )}
            {decisions[OTHER]?.length > 0 && (
              <div className="planning__topic">
                <strong className="muted note">Other</strong>
                <ul className="planning__list planning__list--decided">
                  {lines(decisions[OTHER], "Decision")}
                </ul>
              </div>
            )}
          </section>
          {(planning?.open_questions ?? []).length > 0 && (
            <section className="panel">
              <h3>Still open</h3>
              <p className="muted note">
                If you build now, the assistant uses its suggested default for each.
              </p>
              <ul className="planning__list">
                {lines(
                  (planning?.open_questions ?? []).map((q) => withoutStep(q)),
                  "Open question",
                )}
              </ul>
            </section>
          )}
          {research}
          {documents}
          {stack}
          {buildPlan}
          <div className="row planning__build">
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
                : "Or press Build it at the top to hand the plan to the assistant."}
            </span>
          </div>
        </>
      ) : (
        <>
          <p className="muted note">
            {step.hint}.{" "}
            {viewed !== null && viewed !== current
              ? "You're looking back at an earlier step; tell the assistant if you want to change something here."
              : "The assistant is asking about this now."}
          </p>
          {step.key === "purpose" && goal}
          <div className="planning__grid">
            <PlanCard title="Decided" empty="Nothing decided here yet." kind="decided">
              {lines(decisions[step.key] ?? [], "Decision")}
            </PlanCard>
            <PlanCard title="Still open" empty="Nothing open here." kind="open">
              {lines(questions[step.key] ?? [], "Open question")}
            </PlanCard>
          </div>
          {step.key === "data" && research}
          {step.key === "data" && documents}
          {step.key === "stack" && stack}
          {step.key === "priorities" && buildPlan}
          {/* Everything settled in the other steps, below the one in view:
              folded by step, so it is there to look back on without taking
              the page. */}
          {STEPS.some((s) => s !== step && decisions[s.key]?.length) && (
            <section className="panel">
              <h3>Decided in other steps</h3>
              {STEPS.filter((s) => s !== step && decisions[s.key]?.length).map((s) => (
                <details key={s.key} className="planning__fold">
                  <summary>
                    <strong>{s.label}</strong>
                    <span className="muted note"> · {decisions[s.key].length} decided</span>
                  </summary>
                  <ul className="planning__list planning__list--decided">
                    {lines(decisions[s.key], "Decision")}
                  </ul>
                </details>
              ))}
            </section>
          )}
        </>
      )}

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

// The planning agenda, as the assistant works through it (vision.PlanningSteps).
const STEPS = [
  { key: "purpose", label: "Purpose", hint: "What it's for and who uses it" },
  { key: "features", label: "Features", hint: "What it does, and what done means" },
  { key: "data", label: "Data", hint: "What it reads, connects to, and where it runs" },
  { key: "look", label: "Look & feel", hint: "Layout, colours, and how it feels" },
  { key: "details", label: "Details", hint: "Empty states, errors, and refreshing" },
  { key: "priorities", label: "Priorities", hint: "The first version, and what can wait" },
  { key: "stack", label: "Stack", hint: "What it's built with" },
  { key: "review", label: "Review", hint: "Everything, ready to build" },
] as const;
const OTHER = "other";

function stepIndex(step: string | undefined): number {
  const index = STEPS.findIndex((s) => s.key === step);
  return index < 0 ? 0 : index;
}

// Decisions and questions start with their step ("look: dark theme"); they
// are shown under it without the prefix. Lines without one are "other".
function sortByStep(list: string[]): Record<string, string[]> {
  const sorted: Record<string, string[]> = {};
  for (const line of list) {
    const key = stepOf(line);
    (sorted[key] ??= []).push(withoutStep(line));
  }
  return sorted;
}

function stepOf(line: string): string {
  const match = /^([a-z]+)\s*:/i.exec(line);
  const key = match?.[1].toLowerCase();
  return key && STEPS.some((s) => s.key === key) ? key : OTHER;
}

function withoutStep(line: string): string {
  return stepOf(line) === OTHER ? line : line.slice(line.indexOf(":") + 1).trim();
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
  kind,
  children,
}: {
  title: string;
  empty: string;
  /** Decided lines show as settled; open ones as still to answer. */
  kind?: "decided" | "open";
  children?: React.ReactNode[];
}) {
  return (
    <section className="panel">
      <h3>{title}</h3>
      {children && children.length > 0 ? (
        <ul className={`planning__list ${kind ? `planning__list--${kind}` : ""}`}>{children}</ul>
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
