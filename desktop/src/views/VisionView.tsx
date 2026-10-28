import { useCallback, useEffect, useRef, useState } from "react";
import {
  getVision,
  getOpenVisionSession,
  getVisionSession,
  saveVision,
  startVisionWorkOrder,
  setUpProject,
  addVisionItem,
} from "../api/vision";
import { listFeatures } from "../api/features";
import { getToolchainPresets } from "../api/toolchains";
import { ApiError } from "../api/client";
import { startAutopilot } from "../api/assistant";
import { ASSISTANT_CHANGED, WORK_CHANGED } from "./AssistantDrawer";
import { ORDER_STATE_LABEL } from "./chatActivity";
import { Markdown } from "./Markdown";
import { PlanningWorkspace } from "./PlanningWorkspace";
import { openAssistantOn } from "../attention/askAssistant";
import { rememberIdea, rememberedIdea } from "./visionSession";
import type {
  Feature,
  Vision,
  VisionItem,
  VisionMode,
  VisionPlan,
  VisionSession,
  VisionStack,
  ProjectToolchain,
  ToolchainPreset,
} from "../api/types";

const MAX_DEPTH = 3;
const VISION_SOURCE_LABELS: Record<Vision["source"], string> = {
  assistant: "Written by the assistant",
  user: "Edited by you",
  repository: "Came with the app",
};

// The Vision page (ADR-020): the project's vision statement on top and the
// prioritized plan below. The assistant writes it from a description or after
// talking it through; the user edits it or has it rewritten. Each item without
// sub-items starts exactly one work order.
export function VisionView({
  projectId,
  onOpenOrder,
  setup,
}: {
  projectId: string;
  onOpenOrder: (featureId: string) => void;
  /** A new project without a stack: the recommended stack sets it up. */
  setup?: { onStarted: (workOrderId: string, toolchain: ProjectToolchain) => void };
}) {
  const [vision, setVision] = useState<Vision | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [features, setFeatures] = useState<Map<string, Feature>>(new Map());
  const [session, setSession] = useState<VisionSession | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState<VisionPlan | null>(null);
  const [rewriting, setRewriting] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);

  const loadVision = useCallback(async () => {
    try {
      setVision(await getVision(projectId));
    } catch (e) {
      if (e instanceof ApiError && e.code === "vision_not_found") setVision(null);
      else setError(describe(e));
    }
    try {
      setFeatures(new Map((await listFeatures(projectId)).map((f) => [f.id, f])));
    } catch {
      // Statuses are a convenience; the plan still shows.
    }
    setLoaded(true);
  }, [projectId]);

  useEffect(() => {
    setVision(null);
    setSession(null);
    setDraft(null);
    setRewriting(false);
    setLoaded(false);
    setError(null);
    void loadVision();
    // Reopen a conversation that is still running or waiting for the user.
    getOpenVisionSession(projectId)
      .then(setSession)
      .catch(() => undefined);
  }, [projectId, loadVision]);

  // Work orders move on by themselves (agents, autopilot), and items join
  // the vision from the assistant: the page follows, as the rail does.
  useEffect(() => {
    const refresh = () => void loadVision();
    const timer = setInterval(refresh, 10_000);
    window.addEventListener(WORK_CHANGED, refresh);
    return () => {
      clearInterval(timer);
      window.removeEventListener(WORK_CHANGED, refresh);
    };
  }, [loadVision]);

  // Follow the assistant while it works.
  const sessionId = session?.id;
  const sessionRunning = session?.status === "running";
  useEffect(() => {
    if (!sessionId || !sessionRunning) return;
    const timer = setInterval(() => {
      getVisionSession(projectId, sessionId)
        .then((s) => {
          setSession(s);
          if (s.status === "written") {
            setSession(null);
            setRewriting(false);
            void loadVision();
          }
        })
        .catch((e) => setError(describe(e)));
    }, 2000);
    return () => clearInterval(timer);
  }, [projectId, sessionId, sessionRunning, loadVision]);

  // The vision is written through the assistant (ADR-035): it talks it
  // through as far as needed and has the vision writer write it.
  const start = (mode: VisionMode, message: string) => {
    rememberIdea(projectId, null);
    setRewriting(false);
    openAssistantOn(
      { kind: "vision" },
      mode === "describe"
        ? `Write the vision from my description:\n\n${message}`
        : `Let's talk the vision through. My idea:\n\n${message}`,
    );
  };

  const startOrder = async (item: VisionItem) => {
    setBusy(item.id);
    setError(null);
    try {
      const { work_order, vision: linked } = await startVisionWorkOrder(projectId, item.id);
      setVision(linked);
      onOpenOrder(work_order.id);
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(null);
    }
  };

  const save = async () => {
    if (!draft) return;
    setBusy("save");
    setError(null);
    try {
      setVision(await saveVision(projectId, trimPlan(draft), vision?.revision ?? 0));
      setDraft(null);
    } catch (e) {
      setError(
        e instanceof ApiError && e.code === "vision_conflict"
          ? "The vision changed while you edited it. Cancel to see the latest version."
          : describe(e),
      );
    } finally {
      setBusy(null);
    }
  };

  if (!loaded) return <p className="muted">Loading…</p>;
  // A vision being planned is the planning workspace (planning mode).
  if (vision?.planning?.status === "planning") {
    return <PlanningWorkspace projectId={projectId} vision={vision} onChanged={setVision} />;
  }

  const plan = vision?.plan;
  const lowConfidence = plan && plan.confidence !== "high" && plan.guesses.length > 0;

  return (
    <div className="vision">
      <div className="vision__head">
        <h2>Vision</h2>
        {vision && !draft && !session && (
          <div className="row">
            <button className="ghost" onClick={() => setDraft(structuredClone(vision.plan))}>
              Edit
            </button>
            <button className="ghost" onClick={() => setRewriting((r) => !r)}>
              Rewrite with the assistant
            </button>
          </div>
        )}
      </div>
      {error && <div className="banner banner--error">{error}</div>}
      {vision?.planning?.status === "paused" && (
        <p className="muted">
          Planning is stopped. The plan is kept as it is; resume it from the assistant.
        </p>
      )}

      {session ? (
        <VisionConversation
          session={session}
          onClose={() => {
            setSession(null);
          }}
        />
      ) : !vision || rewriting ? (
        <WriteVision
          rewriting={!!vision}
          initial={vision ? "" : (rememberedIdea(projectId) ?? "")}
          busy={busy}
          onStart={(mode, message) => start(mode, message)}
          onCancel={vision ? () => setRewriting(false) : undefined}
        />
      ) : draft ? (
        <VisionEditor
          plan={draft}
          onChange={setDraft}
          saving={busy === "save"}
          onSave={() => void save()}
          onCancel={() => {
            setDraft(null);
            setError(null);
            void loadVision();
          }}
        />
      ) : (
        plan && (
          <>
            {setup && plan.stack && (
              <ProjectSetupCard
                projectId={projectId}
                stack={plan.stack}
                onStarted={setup.onStarted}
              />
            )}
            <section className="panel vision__statement">
              <Markdown text={plan.statement} />
              <p className="muted vision__source">
                {VISION_SOURCE_LABELS[vision.source]} ·{" "}
                {new Date(vision.updated_at).toLocaleString()}
              </p>
            </section>
            {lowConfidence && (
              <div
                className={`banner ${plan.confidence === "low" ? "banner--warn" : ""} vision__guesses`}
              >
                <p>
                  {plan.confidence === "low"
                    ? "The assistant wrote this plan but isn't confident it had everything it needed. It guessed:"
                    : "The assistant guessed some things:"}
                </p>
                <ul>
                  {plan.guesses.map((g) => (
                    <li key={g}>{g}</li>
                  ))}
                </ul>
                <button
                  onClick={() =>
                    start(
                      "converse",
                      "Go through the guesses in the current vision with me, then rewrite it with my answers.",
                    )
                  }
                  disabled={busy !== null}
                >
                  Answer these
                </button>
              </div>
            )}
            {plan.priorities.length === 0 && <p className="muted">The plan has no items yet.</p>}
            {[...plan.priorities]
              .sort((a, b) => a.level - b.level)
              .map((priority) => (
                <section className="panel vision__priority" key={priority.level}>
                  <PriorityAutopilot projectId={projectId} level={priority.level} />
                  <ItemList
                    items={priority.items}
                    prefix={`P${priority.level}`}
                    features={features}
                    busy={busy}
                    onStart={(item) => void startOrder(item)}
                    onOpen={onOpenOrder}
                  />
                </section>
              ))}
            <OtherWork
              projectId={projectId}
              plan={plan}
              features={features}
              onOpen={onOpenOrder}
              onAdded={setVision}
            />
          </>
        )
      )}
    </div>
  );
}

// Work done outside the plan: every work order and session not linked to a
// vision item (closed ones aside), derived so it is never out of date. One that turns out to be
// real direction can join the vision as an item.
function OtherWork({
  projectId,
  plan,
  features,
  onOpen,
  onAdded,
}: {
  projectId: string;
  plan: VisionPlan;
  features: Map<string, Feature>;
  onOpen: (featureId: string) => void;
  onAdded: (vision: Vision) => void;
}) {
  const [adding, setAdding] = useState<string | null>(null);
  const [showAll, setShowAll] = useState(false);
  const linked = new Set<string>();
  const collect = (items: VisionItem[]) =>
    items.forEach((item) => {
      if (item.feature_id) linked.add(item.feature_id);
      collect(item.items ?? []);
    });
  plan.priorities.forEach((p) => collect(p.items));
  const other = [...features.values()]
    .filter((f) => !f.background && f.state !== "cancelled" && !linked.has(f.id))
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at));
  if (other.length === 0) return null;
  const shown = showAll ? other : other.slice(0, 6);
  return (
    <section className="panel vision__priority vision__other">
      <h3>Other work</h3>
      <p className="muted note">Work orders and sessions outside the vision.</p>
      <ul className="vision__other-list">
        {shown.map((f) => (
          <li key={f.id}>
            <div className="vision__row">
              <div className="vision__text">
                <strong>{f.title}</strong>
                <span className="muted note">
                  {f.kind === "session" ? "Session · " : ""}
                  {ORDER_STATE_LABEL[f.state]}
                </span>
              </div>
              <div className="row vision__actions">
                <button className="ghost" onClick={() => onOpen(f.id)}>
                  Open
                </button>
                {f.kind !== "session" && (
                  <button
                    className="ghost"
                    onClick={() => setAdding(adding === f.id ? null : f.id)}
                  >
                    Add to vision
                  </button>
                )}
              </div>
            </div>
            {adding === f.id && (
              <AddToVision
                projectId={projectId}
                plan={plan}
                work={f}
                onAdded={(v) => {
                  setAdding(null);
                  onAdded(v);
                }}
                onCancel={() => setAdding(null)}
              />
            )}
          </li>
        ))}
      </ul>
      {other.length > shown.length && (
        <button className="linkish" onClick={() => setShowAll(true)}>
          Show all {other.length}
        </button>
      )}
    </section>
  );
}

function AddToVision({
  projectId,
  plan,
  work,
  onAdded,
  onCancel,
}: {
  projectId: string;
  plan: VisionPlan;
  work: Feature;
  onAdded: (vision: Vision) => void;
  onCancel: () => void;
}) {
  const levels = plan.priorities.map((p) => p.level).sort((a, b) => a - b);
  const next = levels.length ? levels[levels.length - 1] + 1 : 0;
  const [priority, setPriority] = useState(levels.length ? levels[levels.length - 1] : 0);
  const [title, setTitle] = useState(work.title);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const add = async () => {
    setBusy(true);
    setError(null);
    try {
      const { vision } = await addVisionItem(projectId, {
        priority,
        title: title.trim(),
        description: "",
        work_order_id: work.id,
      });
      onAdded(vision);
    } catch (e) {
      setError(describe(e));
      setBusy(false);
    }
  };
  return (
    <div className="vision__add">
      <select
        value={priority}
        onChange={(e) => setPriority(Number(e.target.value))}
        disabled={busy}
      >
        {[...levels, ...(next <= 9 ? [next] : [])].map((level) => (
          <option key={level} value={level}>
            P{level}
          </option>
        ))}
      </select>
      <input type="text" value={title} onChange={(e) => setTitle(e.target.value)} disabled={busy} />
      <button className="primary" onClick={() => void add()} disabled={busy || !title.trim()}>
        {busy ? "Adding…" : "Add"}
      </button>
      <button className="ghost" onClick={onCancel} disabled={busy}>
        Cancel
      </button>
      {error && <div className="banner banner--error">{error}</div>}
    </div>
  );
}

// The last step of creating a project: confirm the recommended stack, and
// Commitarium scaffolds the app on it with its preview in one work order.
function ProjectSetupCard({
  projectId,
  stack,
  onStarted,
}: {
  projectId: string;
  stack: VisionStack;
  onStarted: (workOrderId: string, toolchain: ProjectToolchain) => void;
}) {
  const [presets, setPresets] = useState<ToolchainPreset[]>([]);
  const [chosen, setChosen] = useState<string[]>(stack.presets);
  const [frameworks, setFrameworks] = useState(stack.frameworks);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    getToolchainPresets()
      .then((r) => setPresets(r.presets))
      .catch((e) => setError(describe(e)));
  }, []);

  const toggle = (id: string) =>
    setChosen((c) => (c.includes(id) ? c.filter((p) => p !== id) : [...c, id]));

  const start = async () => {
    setBusy(true);
    setError(null);
    try {
      const { work_order, toolchain } = await setUpProject(projectId, chosen, frameworks);
      onStarted(work_order.id, toolchain);
    } catch (e) {
      setError(describe(e));
      setBusy(false);
    }
  };

  return (
    <section className="panel vision__setup">
      <h3>Set the project up</h3>
      <p className="muted">
        {stack.reason || "The assistant recommends this stack."} Commitarium scaffolds a minimal app
        on it, with tests and a live-reloading preview, before the plan's items start.
      </p>
      <div className="vision__presets">
        {presets.map((p) => (
          <button
            key={p.id}
            className={`vision__preset ${chosen.includes(p.id) ? "vision__preset--on" : ""}`}
            onClick={() => toggle(p.id)}
            title={p.description}
            disabled={busy}
          >
            {p.display_name}
          </button>
        ))}
      </div>
      <label className="field">
        <span>Frameworks</span>
        <input
          type="text"
          value={frameworks}
          onChange={(e) => setFrameworks(e.target.value)}
          placeholder="React with Vite; FastAPI"
          disabled={busy}
        />
      </label>
      {error && <div className="banner banner--error">{error}</div>}
      <button
        className="primary"
        onClick={() => void start()}
        disabled={busy || chosen.length === 0}
      >
        {busy ? "Starting…" : "Set the project up"}
      </button>
    </section>
  );
}

function WriteVision({
  rewriting,
  initial,
  busy,
  onStart,
  onCancel,
}: {
  rewriting: boolean;
  initial: string;
  busy: string | null;
  onStart: (mode: VisionMode, message: string) => void;
  onCancel?: () => void;
}) {
  const [text, setText] = useState(initial);
  const message = text.trim();
  return (
    <section className="panel vision__write">
      <p>
        {rewriting
          ? "Tell the assistant what should change. It rewrites the vision from the current one, keeping the work orders already started."
          : "Describe the app: who it's for, what it should do, and what matters most. The assistant writes the vision and a prioritized plan from it."}
      </p>
      <textarea
        rows={6}
        value={text}
        onChange={(e) => setText(e.target.value)}
        placeholder={
          rewriting
            ? "What should change?"
            : "A to-do app for my family, shared lists, works on phones…"
        }
      />
      <div className="row">
        <button
          className="primary"
          onClick={() => onStart("describe", message)}
          disabled={!message || busy !== null}
          title="The assistant writes it at once and lists what it had to guess."
        >
          Write it from my description
        </button>
        <button
          onClick={() => onStart("converse", message)}
          disabled={!message || busy !== null}
          title="The assistant asks what it needs to know first."
        >
          Talk it through
        </button>
        {onCancel && (
          <button className="ghost" onClick={onCancel} disabled={busy !== null}>
            Cancel
          </button>
        )}
      </div>
    </section>
  );
}

// A vision being written: the conversation shows as it was, read-only; the
// user talks it through with the assistant (ADR-035).
function VisionConversation({ session, onClose }: { session: VisionSession; onClose: () => void }) {
  const chatRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    const el = chatRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [session.messages.length]);

  return (
    <section className="panel vision__conversation">
      <div className="chat" ref={chatRef}>
        {session.messages.map((m, index) => (
          <div key={index} className={`msg ${m.role === "user" ? "msg--user" : "msg--lead"}`}>
            <span className="msg__who">{m.role === "user" ? "You" : "Assistant"}</span>
            <div className="msg__text">
              <Markdown text={m.text} />
            </div>
          </div>
        ))}
      </div>
      {session.status === "running" && (
        <p className="muted status-line">The vision is being written…</p>
      )}
      {session.status === "failed" && (
        <div className="banner banner--warn">
          <span>Writing the vision stopped before it was done.</span>
          <button onClick={onClose}>Start again</button>
        </div>
      )}
      {session.status === "waiting_for_user" && (
        <div className="row">
          <button
            onClick={() => openAssistantOn({ kind: "vision" }, "Let's continue with the vision.")}
          >
            Continue with the assistant
          </button>
        </div>
      )}
    </section>
  );
}

function ItemList({
  items,
  prefix,
  features,
  busy,
  onStart,
  onOpen,
}: {
  items: VisionItem[];
  prefix: string;
  features: Map<string, Feature>;
  busy: string | null;
  onStart: (item: VisionItem) => void;
  onOpen: (featureId: string) => void;
}) {
  return (
    <ol className="vision__items">
      {items.map((item, index) => {
        const label = `${prefix}.${index + 1}`;
        const leaf = !item.items?.length;
        const linked = item.feature_id ? features.get(item.feature_id) : undefined;
        const done = allDone(item, features);
        return (
          <li key={item.id} className="vision__item">
            <div className="vision__row">
              <span className="vision__label">{label}</span>
              <div className="vision__text">
                <strong>{item.title}</strong>
                {item.description && <p className="muted">{item.description}</p>}
              </div>
              <div className="row vision__actions">
                {done && <span className="pill pill--ok">Done</span>}
                {leaf && item.feature_id && !done && (
                  <span className="pill">
                    {linked ? ORDER_STATE_LABEL[linked.state] : "Work order"}
                  </span>
                )}
                {leaf &&
                  !(item.done && !item.feature_id) &&
                  (item.feature_id ? (
                    <button className="ghost" onClick={() => onOpen(item.feature_id!)}>
                      Open
                    </button>
                  ) : (
                    <button onClick={() => onStart(item)} disabled={busy !== null}>
                      {busy === item.id ? "Creating…" : "Start work order"}
                    </button>
                  ))}
              </div>
            </div>
            {!leaf && (
              <ItemList
                items={item.items!}
                prefix={label}
                features={features}
                busy={busy}
                onStart={onStart}
                onOpen={onOpen}
              />
            )}
          </li>
        );
      })}
    </ol>
  );
}

// An item is done when the project already delivered it or its work order
// merged; a group is done when every item under it is.
function allDone(item: VisionItem, features: Map<string, Feature>): boolean {
  if (!item.items?.length)
    return !!item.done || features.get(item.feature_id ?? "")?.state === "completed";
  return item.items.every((child) => allDone(child, features));
}

function VisionEditor({
  plan,
  onChange,
  saving,
  onSave,
  onCancel,
}: {
  plan: VisionPlan;
  onChange: (plan: VisionPlan) => void;
  saving: boolean;
  onSave: () => void;
  onCancel: () => void;
}) {
  const ids = new Set<string>();
  const collect = (items: VisionItem[]) =>
    items.forEach((item) => {
      ids.add(item.id);
      collect(item.items ?? []);
    });
  plan.priorities.forEach((p) => collect(p.items));
  const newItem = (): VisionItem => {
    let id = "item";
    for (let n = 2; ids.has(id); n++) id = `item-${n}`;
    ids.add(id);
    return { id, title: "", description: "" };
  };

  const setPriorityItems = (level: number, items: VisionItem[]) =>
    onChange({
      ...plan,
      priorities: plan.priorities.map((p) => (p.level === level ? { ...p, items } : p)),
    });
  const nextLevel = plan.priorities.reduce((max, p) => Math.max(max, p.level + 1), 0);
  const sorted = [...plan.priorities].sort((a, b) => a.level - b.level);
  const incomplete = hasEmptyTitle(plan) || !plan.statement.trim();

  return (
    <div className="vision__editor">
      <section className="panel">
        <label className="field">
          <span>Vision</span>
          <textarea
            rows={4}
            value={plan.statement}
            onChange={(e) => onChange({ ...plan, statement: e.target.value })}
          />
        </label>
      </section>
      {sorted.map((priority) => (
        <section className="panel vision__priority" key={priority.level}>
          <div className="vision__head">
            <h3>P{priority.level}</h3>
            <button
              className="ghost danger"
              onClick={() =>
                onChange({ ...plan, priorities: plan.priorities.filter((p) => p !== priority) })
              }
            >
              Remove P{priority.level}
            </button>
          </div>
          <ItemEditor
            items={priority.items}
            prefix={`P${priority.level}`}
            depth={1}
            newItem={newItem}
            onChange={(items) => setPriorityItems(priority.level, items)}
          />
        </section>
      ))}
      <div className="row">
        {nextLevel <= 9 && (
          <button
            className="ghost"
            onClick={() =>
              onChange({
                ...plan,
                priorities: [...plan.priorities, { level: nextLevel, items: [newItem()] }],
              })
            }
          >
            Add P{nextLevel}
          </button>
        )}
        <span className="rail__spacer" />
        <button className="ghost" onClick={onCancel} disabled={saving}>
          Cancel
        </button>
        <button className="primary" onClick={onSave} disabled={saving || incomplete}>
          {saving ? "Saving…" : "Save"}
        </button>
      </div>
    </div>
  );
}

function ItemEditor({
  items,
  prefix,
  depth,
  newItem,
  onChange,
}: {
  items: VisionItem[];
  prefix: string;
  depth: number;
  newItem: () => VisionItem;
  onChange: (items: VisionItem[]) => void;
}) {
  const update = (index: number, item: VisionItem) =>
    onChange(items.map((existing, i) => (i === index ? item : existing)));
  const move = (index: number, by: number) => {
    const next = [...items];
    const [item] = next.splice(index, 1);
    next.splice(index + by, 0, item);
    onChange(next);
  };
  return (
    <ol className="vision__items">
      {items.map((item, index) => {
        const label = `${prefix}.${index + 1}`;
        return (
          <li key={item.id} className="vision__item">
            <div className="vision__row">
              <span className="vision__label">{label}</span>
              <div className="vision__text">
                <input
                  type="text"
                  value={item.title}
                  placeholder="Title"
                  onChange={(e) => update(index, { ...item, title: e.target.value })}
                />
                <textarea
                  rows={2}
                  value={item.description}
                  placeholder="Description"
                  onChange={(e) => update(index, { ...item, description: e.target.value })}
                />
              </div>
              <div className="vision__actions vision__actions--edit">
                <button className="ghost" onClick={() => move(index, -1)} disabled={index === 0}>
                  ↑
                </button>
                <button
                  className="ghost"
                  onClick={() => move(index, 1)}
                  disabled={index === items.length - 1}
                >
                  ↓
                </button>
                {!item.items?.length && (
                  <label
                    className="toggle vision__done"
                    title="Already delivered; no work order needed"
                  >
                    <input
                      type="checkbox"
                      checked={!!item.done}
                      onChange={(e) => update(index, { ...item, done: e.target.checked })}
                    />
                    Done
                  </label>
                )}
                {depth < MAX_DEPTH && !item.feature_id && !item.done && (
                  <button
                    className="ghost"
                    onClick={() =>
                      update(index, { ...item, items: [...(item.items ?? []), newItem()] })
                    }
                  >
                    Add sub-item
                  </button>
                )}
                <button
                  className="ghost danger"
                  onClick={() => onChange(items.filter((_, i) => i !== index))}
                  title={item.feature_id ? "Its work order stays." : undefined}
                >
                  Remove
                </button>
              </div>
            </div>
            {!!item.items?.length && (
              <ItemEditor
                items={item.items}
                prefix={label}
                depth={depth + 1}
                newItem={newItem}
                onChange={(children) => update(index, { ...item, items: children })}
              />
            )}
          </li>
        );
      })}
      <li>
        <button className="ghost" onClick={() => onChange([...items, newItem()])}>
          Add item
        </button>
      </li>
    </ol>
  );
}

function trimPlan(plan: VisionPlan): VisionPlan {
  const trim = (items: VisionItem[]): VisionItem[] =>
    items.map((item) => ({
      ...item,
      title: item.title.trim(),
      description: item.description.trim(),
      items: item.items?.length ? trim(item.items) : undefined,
    }));
  return {
    ...plan,
    statement: plan.statement.trim(),
    priorities: plan.priorities.map((p) => ({ ...p, items: trim(p.items) })),
  };
}

function hasEmptyTitle(plan: VisionPlan): boolean {
  const empty = (items: VisionItem[]): boolean =>
    items.some((item) => !item.title.trim() || empty(item.items ?? []));
  return plan.priorities.some((p) => empty(p.items));
}

function describe(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === "vision_unavailable") return "The vision assistant is unavailable: " + e.message;
    return e.message;
  }
  return String(e);
}

// A priority's heading, with autopilot: the assistant finishes the
// priority's open items, creating and starting their work orders itself.
function PriorityAutopilot({ projectId, level }: { projectId: string; level: number }) {
  const [confirming, setConfirming] = useState(false);
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const start = async () => {
    setStarting(true);
    setError(null);
    try {
      await startAutopilot(projectId, level);
      setConfirming(false);
      window.dispatchEvent(new Event(ASSISTANT_CHANGED));
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    } finally {
      setStarting(false);
    }
  };

  return (
    <>
      <div className="vision__priority-head">
        <h3>P{level}</h3>
        {!confirming && (
          <button className="ghost" onClick={() => setConfirming(true)}>
            Autopilot
          </button>
        )}
      </div>
      {confirming && (
        <div className="vision__autopilot">
          <p className="muted note">
            Let the assistant finish P{level}? It creates and starts work orders for the open items
            without asking, except with premium models, and tells you when one needs you. You can
            stop it at any time.
          </p>
          <div className="row">
            <button className="primary" onClick={() => void start()} disabled={starting}>
              {starting ? "Starting…" : "Start autopilot"}
            </button>
            <button className="ghost" onClick={() => setConfirming(false)} disabled={starting}>
              Cancel
            </button>
          </div>
        </div>
      )}
      {error && <div className="banner banner--error">{error}</div>}
    </>
  );
}
