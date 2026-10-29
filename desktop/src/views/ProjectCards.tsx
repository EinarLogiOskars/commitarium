import { useCallback, useEffect, useMemo, useState } from "react";
import { getAssistantSettings } from "../api/assistant";
import { getAgentsAtWork } from "../api/projects";
import { ApiError } from "../api/client";
import { listFeatures } from "../api/features";
import { getProjectToolchain } from "../api/toolchains";
import type {
  AgentAtWork,
  AttentionItem,
  Feature,
  Project,
  ProjectToolchain,
  RunningWorkOrder,
  Vision,
} from "../api/types";
import { finishPlanning, getVision } from "../api/vision";
import { openExternal } from "../ipc";
import { WORK } from "../vocab";
import { WORK_CHANGED } from "./ChatView";
import { chatPlanning, planPhase } from "./planMode";
import { ASSISTANT_CHANGED, WRITE_PLAN } from "./chatEvents";
import { usePlanSwitch } from "./usePlanSwitch";
import { ActivityDot } from "./chatActivity";
import { CompactProjectSync } from "./ProjectSyncCard";
import { Overlay } from "./Overlay";
import { PlanDocumentView } from "./PlanPage";
import { PlanInsights } from "./PlanInsights";
import { StopPlanDialog } from "./StopPlanDialog";
import {
  agentLabel,
  agentRoleLabel,
  agentSubject,
  DEFAULT_MAX_RUNNING,
  priorityProgress,
  shownAgents,
  workBoard,
  type WorkRow,
} from "./projectCardData";
import type { PageMode, PageTarget } from "./pageTarget";
import { useModelName } from "./useModelName";
import { PREVIEW_STATE, primaryUrl, usePreview } from "./usePreview";

const POLL_MS = 10_000;

// The cards beside a project's chat (ADR-038): the project, its work orders,
// the agents at work, the preview, and sync. They read the same records the
// project's pages do, again every few seconds and whenever the assistant
// changes work. Their links open those pages in the centre.
export function ProjectCards({
  projectId,
  project,
  chat,
  attention,
  onOpenPage,
}: {
  projectId: string;
  /** The project from the shell's list; undefined while it loads. */
  project: Project | undefined;
  /** The chat on screen, which the plan switch plans in. */
  chat: string | undefined;
  attention: { items: AttentionItem[]; running: RunningWorkOrder[] };
  /** Opens one of the project's pages in the centre, over the chat. */
  onOpenPage: (page: PageTarget) => void;
}) {
  const data = useProjectData(projectId);
  const items = useMemo(
    () => attention.items.filter((i) => i.project_id === projectId),
    [attention.items, projectId],
  );
  const running = useMemo(
    () => attention.running.filter((r) => r.project_id === projectId),
    [attention.running, projectId],
  );
  const open = (mode: PageMode) => onOpenPage({ mode });
  const openOrder = (orderId: string) => onOpenPage({ mode: "order", orderId });
  const hasRepo = !!project?.forgejo_repository;

  return (
    <>
      <PlanCard
        projectId={projectId}
        name={project?.name ?? ""}
        chat={chat}
        vision={data.vision}
        onChanged={data.reload}
      />
      <ProjectCard data={data} hasRepo={hasRepo} onOpen={open} />
      <WorkOrdersCard
        features={data.features}
        items={items}
        running={running}
        maxRunning={data.maxRunning}
        onOpen={openOrder}
        onNew={() => open("new")}
        onAll={() => open("work")}
      />
      <AgentsCard projectId={projectId} onOpen={openOrder} />
      {hasRepo && (
        <PreviewCard
          projectId={projectId}
          appName={project?.app ? project.name : undefined}
          toolchain={data.toolchain}
          onOpenPage={() => open("preview")}
        />
      )}
      {hasRepo && (
        <section className="shell-card">
          <h2 className="shell-card__head">Sync</h2>
          <CompactProjectSync projectId={projectId} onOpenFull={() => open("sync")} />
        </section>
      )}
    </>
  );
}

interface ProjectData {
  features: Feature[] | null;
  /** null when the project has no vision yet; undefined until read. */
  vision: Vision | null | undefined;
  toolchain: ProjectToolchain | null;
  maxRunning: number;
  reload: () => void;
}

// The shell mounts the cards afresh for each project, so nothing here resets.
function useProjectData(projectId: string): ProjectData {
  const [features, setFeatures] = useState<Feature[] | null>(null);
  const [vision, setVision] = useState<Vision | null | undefined>(undefined);
  const [toolchain, setToolchain] = useState<ProjectToolchain | null>(null);
  const [maxRunning, setMaxRunning] = useState(DEFAULT_MAX_RUNNING);

  const load = useCallback(() => {
    listFeatures(projectId)
      .then(setFeatures)
      .catch(() => undefined);
    getVision(projectId)
      .then(setVision)
      .catch((e) => {
        if (e instanceof ApiError && e.status === 404) setVision(null);
      });
    // Absent on older coordinators: the card then shows no stack.
    getProjectToolchain(projectId)
      .then(setToolchain)
      .catch(() => undefined);
  }, [projectId]);

  useEffect(() => {
    load();
    const timer = setInterval(load, POLL_MS);
    window.addEventListener(WORK_CHANGED, load);
    return () => {
      clearInterval(timer);
      window.removeEventListener(WORK_CHANGED, load);
    };
  }, [load]);

  // The ceiling on running work is the user's setting (ADR-036).
  useEffect(() => {
    getAssistantSettings()
      .then((s) =>
        setMaxRunning(s.max_running && s.max_running > 0 ? s.max_running : DEFAULT_MAX_RUNNING),
      )
      .catch(() => undefined);
  }, []);

  return { features, vision, toolchain, maxRunning, reload: load };
}

function ProjectCard({
  data,
  hasRepo,
  onOpen,
}: {
  data: ProjectData;
  hasRepo: boolean;
  onOpen: (mode: PageMode) => void;
}) {
  const { vision, features, toolchain } = data;
  const progress = vision && features ? priorityProgress(vision.plan, features) : [];
  // The stack the project runs on, or the one its vision recommends before
  // it is set up.
  const tools =
    toolchain?.status === "configured"
      ? Object.entries(toolchain.tools).map(([tool, version]) => `${tool} ${version}`)
      : (vision?.plan.stack?.presets ?? []);
  const frameworks = vision?.plan.stack?.frameworks;

  return (
    <section className="shell-card">
      <h2 className="shell-card__head">Project</h2>
      {vision === undefined ? (
        <p className="muted shell-card__text">Loading…</p>
      ) : vision === null ? (
        <p className="muted shell-card__text">No plan yet. The vision says what to build first.</p>
      ) : progress.length === 0 ? (
        <p className="muted shell-card__text">The plan has nothing in it yet.</p>
      ) : (
        <ul className="shell-progress" aria-label="Progress by priority">
          {progress.map((p) => (
            <li key={p.level} className="shell-progress__row">
              <span className="shell-progress__level">P{p.level}</span>
              <span className="shell-progress__bar" aria-hidden>
                <span
                  className={`shell-progress__fill ${p.done === p.total ? "shell-progress__fill--done" : ""}`}
                  style={{ width: `${(p.done / p.total) * 100}%` }}
                />
              </span>
              <span className="muted shell-progress__count">
                {p.done}/{p.total}
              </span>
            </li>
          ))}
        </ul>
      )}
      {(tools.length > 0 || frameworks) && (
        <div className="shell-stack">
          {tools.map((tool) => (
            <span key={tool} className="pill shell-stack__tool">
              {tool}
            </span>
          ))}
          {frameworks && (
            <p className="muted shell-card__text shell-stack__frameworks" title={frameworks}>
              {frameworks}
            </p>
          )}
        </div>
      )}
      <div className="shell-card__links">
        <button className="shell-card__link" onClick={() => onOpen("vision")}>
          {vision === null ? "Plan it" : "Open the vision"}
        </button>
        {toolchain?.status === "needs_setup" ? (
          <button className="shell-card__link" onClick={() => onOpen("stack")}>
            Set up the stack
          </button>
        ) : (
          toolchain && (
            <button className="shell-card__link" onClick={() => onOpen("stack")}>
              Stack
            </button>
          )
        )}
        {hasRepo && (
          <button className="shell-card__link" onClick={() => onOpen("repository")}>
            Repository
          </button>
        )}
        <button className="shell-card__link" onClick={() => onOpen("settings")}>
          Settings
        </button>
      </div>
    </section>
  );
}

function WorkOrdersCard({
  features,
  items,
  running,
  maxRunning,
  onOpen,
  onNew,
  onAll,
}: {
  features: Feature[] | null;
  items: AttentionItem[];
  running: RunningWorkOrder[];
  maxRunning: number;
  onOpen: (featureId: string) => void;
  onNew: () => void;
  /** Every work order and session, done ones included. */
  onAll: () => void;
}) {
  const board = useMemo(
    () => (features ? workBoard(features, items, running) : null),
    [features, items, running],
  );

  return (
    <section className="shell-card">
      <div className="shell-card__top">
        <h2 className="shell-card__head">Work orders</h2>
        {board && board.total > 0 && (
          <span className="muted shell-card__meta">
            {board.done} of {board.total} done
          </span>
        )}
      </div>
      {!board ? (
        <p className="muted shell-card__text">Loading…</p>
      ) : board.total === 0 ? (
        <p className="muted shell-card__text">None yet. Ask in the chat for something to build.</p>
      ) : (
        <>
          {board.running.length > 0 && (
            <p className="muted shell-card__sub">
              {board.running.length} working · up to {maxRunning} at once
            </p>
          )}
          {board.running.length + board.waiting.length > 0 ? (
            <ul className="shell-orders">
              {[...board.running, ...board.waiting].map((row) => (
                <OrderRow key={row.id} row={row} onOpen={onOpen} />
              ))}
            </ul>
          ) : (
            board.queue.length === 0 && (
              <p className="muted shell-card__text">Nothing is running now.</p>
            )
          )}
          {board.queue.length > 0 && (
            <>
              <p className="muted shell-card__sub">Waiting to merge, one at a time</p>
              <ol className="shell-orders">
                {board.queue.map((q) => (
                  <li key={q.id}>
                    <button className="shell-order" onClick={() => onOpen(q.id)} title={q.title}>
                      <span className="shell-order__place">{q.place}</span>
                      <span className="shell-order__text">
                        <span className="shell-order__title">{q.title}</span>
                      </span>
                    </button>
                  </li>
                ))}
              </ol>
            </>
          )}
          {board.notStarted > 0 && (
            <p className="muted shell-card__sub">{board.notStarted} not started</p>
          )}
        </>
      )}
      <div className="shell-card__links">
        <button className="shell-card__link" onClick={onNew}>
          + {WORK.newAction}
        </button>
        <button
          className="shell-card__link"
          onClick={onAll}
          title="Every work order and session, done ones too"
        >
          All work
        </button>
      </div>
    </section>
  );
}

function OrderRow({ row, onOpen }: { row: WorkRow; onOpen: (featureId: string) => void }) {
  return (
    <li>
      <button className="shell-order" onClick={() => onOpen(row.id)} title={row.reason}>
        <ActivityDot
          activity={row.tone === "running" ? "running" : row.tone === "you" ? "waiting" : "idle"}
        />
        <span className="shell-order__text">
          <span className="shell-order__title">{row.title}</span>
          <span
            className={`shell-order__reason ${row.tone === "you" ? "shell-order__reason--you" : ""}`}
          >
            {row.tone === "you" ? `Needs you: ${row.reason}` : row.reason}
          </span>
        </span>
      </button>
    </li>
  );
}

// The agents at work in the project, read again every few seconds and
// whenever the assistant changes work. null until the first read.
function useAgentsAtWork(projectId: string): AgentAtWork[] | null {
  const [agents, setAgents] = useState<AgentAtWork[] | null>(null);
  const load = useCallback(() => {
    getAgentsAtWork(projectId)
      .then((r) => setAgents(r.agents ?? []))
      // An older coordinator has no such route: the card says nobody works.
      .catch((e) => {
        if (e instanceof ApiError && e.status === 404) setAgents([]);
      });
  }, [projectId]);
  useEffect(() => {
    load();
    const timer = setInterval(load, POLL_MS);
    window.addEventListener(WORK_CHANGED, load);
    return () => {
      clearInterval(timer);
      window.removeEventListener(WORK_CHANGED, load);
    };
  }, [load]);
  return agents;
}

// A small card per agent at work, two to a row, pulsing while the agent
// works. "All" opens every one of them as a grid once there are more than
// fit.
function AgentsCard({
  projectId,
  onOpen,
}: {
  projectId: string;
  onOpen: (featureId: string) => void;
}) {
  const agents = useAgentsAtWork(projectId);
  const modelName = useModelName();
  const [showAll, setShowAll] = useState(false);
  const working = agents?.filter((a) => a.working).length ?? 0;
  const { shown, more } = shownAgents(agents ?? []);
  const tile = (agent: AgentAtWork) => (
    <AgentTile
      key={agent.id}
      agent={agent}
      model={modelName(agent.model)}
      onOpen={(id) => {
        setShowAll(false);
        onOpen(id);
      }}
    />
  );

  return (
    <section className="shell-card">
      <div className="shell-card__top">
        <h2 className="shell-card__head">Agents</h2>
        {working > 0 && <span className="muted shell-card__meta">{working} working</span>}
      </div>
      {agents === null ? (
        <p className="muted shell-card__text">Loading…</p>
      ) : agents.length === 0 ? (
        <p className="muted shell-card__text">No agents at work right now.</p>
      ) : (
        <ul className="shell-agents">{shown.map(tile)}</ul>
      )}
      {more > 0 && (
        <button className="shell-card__link shell-agents__all" onClick={() => setShowAll(true)}>
          All ({agents?.length})
        </button>
      )}
      {showAll && agents && (
        <Overlay title="Agents at work" onClose={() => setShowAll(false)}>
          <ul className="shell-agents shell-agents--all">{agents.map(tile)}</ul>
        </Overlay>
      )}
    </section>
  );
}

function AgentTile({
  agent,
  model,
  onOpen,
}: {
  agent: AgentAtWork;
  model: string;
  onOpen: (featureId: string) => void;
}) {
  const subject = agentSubject(agent);
  const status = agent.working ? "Working now" : (agent.detail ?? "Idle");
  const body = (
    <>
      <span className="shell-agent__name">
        <ActivityDot activity={agent.working ? "running" : "idle"} />
        <span className="shell-agent__text">{agentLabel(agent)}</span>
      </span>
      {model && <span className="shell-agent__text shell-agent__model">{model}</span>}
      <span className="shell-agent__text shell-agent__role">{agentRoleLabel(agent.role)}</span>
      {subject && <span className="shell-agent__text shell-agent__subject">{subject}</span>}
    </>
  );
  const className = `shell-agent ${agent.working ? "shell-agent--working" : ""}`;
  const title = subject ? `${subject}: ${status}` : status;
  return (
    <li>
      {agent.work_order_id ? (
        <button className={className} title={title} onClick={() => onOpen(agent.work_order_id!)}>
          {body}
        </button>
      ) : (
        <div className={className} title={title}>
          {body}
        </div>
      )}
    </li>
  );
}

// The preview rail item as a card: its state, Run or Open, Stop, and the
// full Preview page for anything more (setup, logs, data).
function PreviewCard({
  projectId,
  appName,
  toolchain,
  onOpenPage,
}: {
  projectId: string;
  /** An app's preview opens in its own window. */
  appName?: string;
  toolchain: ProjectToolchain | null;
  onOpenPage: () => void;
}) {
  const preview = usePreview(projectId, undefined, appName);
  const state = preview.status?.state ?? "stopped";
  const shown = PREVIEW_STATE[state];
  const url = state === "running" ? primaryUrl(preview.status) : null;
  const active = state === "running" || state === "starting";
  // Like the Preview page, a preview can run once the stack is set up.
  const runnable = toolchain?.status === "configured";

  return (
    <section className="shell-card">
      <div className="shell-card__top">
        <h2 className="shell-card__head">Preview</h2>
        <span className="shell-card__meta shell-preview__state">
          <span
            className={`dot dot--${shown.tone} ${shown.pulse ? "dot--pulse" : ""}`}
            aria-hidden
          />
          {shown.label}
        </span>
      </div>
      {url && (
        <p className="muted shell-card__text shell-preview__url" title={url}>
          {url}
        </p>
      )}
      {!runnable && !active && (
        <p className="muted shell-card__text">The preview runs once the project is set up.</p>
      )}
      {preview.error && <p className="sync-card__error">{preview.error}</p>}
      <div className="row shell-preview__actions">
        {url && (
          <button className="primary" onClick={() => void openExternal(url)}>
            Open
          </button>
        )}
        {runnable && state !== "starting" && (
          <button className={url ? "ghost" : "primary"} onClick={() => void preview.start()}>
            {state === "running" ? "Restart" : state === "failed" ? "Try again" : "Run"}
          </button>
        )}
        {active && (
          <button className="ghost" onClick={() => void preview.stop()}>
            Stop
          </button>
        )}
        <button className="shell-card__link" onClick={onOpenPage}>
          Preview page
        </button>
      </div>
    </section>
  );
}

/** The Plan card (ADR-039, PLAN_MODE_PLAN.md PM4 reshaped): plan mode is a
 * switch on the chat on screen. Switching it on starts, resumes, or moves
 * planning here (a project with no vision yet starts its plan from its
 * name); switching it off asks whether to keep the plan for next time or
 * discard it. While planning, the plan opens over the page (its draft, or
 * Write the plan while still talking), Insights shows the assistant's
 * working notes, and Build it hands a drafted plan to autopilot. */
function PlanCard({
  projectId,
  name,
  chat,
  vision,
  onChanged,
}: {
  projectId: string;
  name: string;
  chat: string | undefined;
  vision: Vision | null | undefined;
  onChanged: () => void;
}) {
  const planning = chatPlanning(vision ?? { planning: undefined }, chat);
  const plan = usePlanSwitch(projectId, name, chat, planning, { onChanged });
  const [shown, setShown] = useState<null | "plan" | "insights">(null);
  const [building, setBuilding] = useState(false);
  const [buildError, setBuildError] = useState<string | null>(null);
  const close = useCallback(() => setShown(null), []);
  if (vision === undefined) return null;
  const drafted = planPhase(vision?.planning) === "drafted";
  const { on } = plan;
  // Build it: the plan locks and its slices go to autopilot.
  const build = async () => {
    setBuilding(true);
    setBuildError(null);
    try {
      await finishPlanning(projectId, true);
      window.dispatchEvent(new Event(WORK_CHANGED));
      window.dispatchEvent(new Event(ASSISTANT_CHANGED));
      onChanged();
    } catch (e) {
      setBuildError(e instanceof Error ? e.message : String(e));
    } finally {
      setBuilding(false);
    }
  };
  const state =
    planning.kind === "here"
      ? planning.round
        ? "Planning next round"
        : "Planning"
      : planning.kind === "elsewhere"
        ? "In another chat — move here"
        : planning.kind === "paused"
          ? "Off · plan kept"
          : "Off";
  return (
    <section className={`shell-card plan-card ${on ? "plan-card--on" : ""}`}>
      <div className="plan-card__row">
        <span className="plan-card__label">
          <span className="plan-card__name">Plan mode</span>
          <span className="plan-card__state">{state}</span>
        </span>
        <button
          role="switch"
          aria-checked={on}
          aria-label="Plan mode"
          className={`plan-switch ${on ? "plan-switch--on" : ""}`}
          onClick={() => void plan.toggle()}
          disabled={plan.busy}
          title={
            on
              ? "Turn plan mode off: keep the plan for next time, or discard it. Nothing is built."
              : planning.kind === "elsewhere"
                ? "Move planning to this chat. The plan stays as it is; the other chat stops planning."
                : planning.kind === "paused"
                  ? "Pick the kept plan up again in this chat."
                  : "Plan with the assistant in this chat. Nothing new is built until you press Build it."
          }
        >
          <span className="plan-switch__knob" aria-hidden />
        </button>
      </div>
      {on && (
        <div className="plan-card__actions">
          <span className="shell-card__links plan-card__links">
            <button
              className="shell-card__link"
              onClick={() => setShown("plan")}
              title={drafted ? "The written plan, to review" : "No draft yet"}
            >
              {drafted ? "Open the plan" : "The plan"}
            </button>
            <button
              className="shell-card__link"
              onClick={() => setShown("insights")}
              title="What the assistant works from: what the plan covers, decisions, open questions, research, and documents"
            >
              Insights
            </button>
            {!drafted && (
              <button
                className="shell-card__link"
                onClick={() => window.dispatchEvent(new Event(WRITE_PLAN))}
                title="Have the assistant write the plan's first draft now"
              >
                Write the plan
              </button>
            )}
          </span>
          {drafted && (
            <button
              className="primary plan-card__build"
              onClick={() => void build()}
              disabled={building}
              title="Lock the plan and hand its slices to autopilot, a work order each."
            >
              {building ? "Starting…" : "Build it"}
            </button>
          )}
        </div>
      )}
      {plan.error && !plan.stopping && <p className="banner banner--error">{plan.error}</p>}
      {buildError && <p className="banner banner--error">{buildError}</p>}
      {shown === "plan" && (
        <Overlay title="Plan" onClose={close}>
          <PlanDocumentView projectId={projectId} onBack={close} inOverlay />
        </Overlay>
      )}
      {shown === "insights" && vision && (
        <Overlay title="Insights" onClose={close}>
          <PlanInsights projectId={projectId} vision={vision} />
        </Overlay>
      )}
      <StopPlanDialog projectId={projectId} plan={plan} />
    </section>
  );
}
