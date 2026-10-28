import { useCallback, useEffect, useMemo, useState } from "react";
import { getAssistantSettings } from "../api/assistant";
import { ApiError } from "../api/client";
import { listFeatures } from "../api/features";
import { getProjectToolchain } from "../api/toolchains";
import type {
  AttentionItem,
  Feature,
  Project,
  ProjectToolchain,
  RunningWorkOrder,
  Vision,
} from "../api/types";
import { getVision } from "../api/vision";
import { openExternal } from "../ipc";
import { OPEN_VISION, WORK_CHANGED } from "./ChatView";
import { ActivityDot } from "./chatActivity";
import { CompactProjectSync } from "./ProjectSyncCard";
import { DEFAULT_MAX_RUNNING, priorityProgress, workBoard, type WorkRow } from "./projectCardData";
import { PREVIEW_STATE, primaryUrl, usePreview } from "./usePreview";

const POLL_MS = 10_000;

// The cards beside a project's chat (ADR-038): the project, its work orders,
// the agents at work, the preview, and sync. They read the same records the
// project page does, again every few seconds and whenever the assistant
// changes work.
export function ProjectCards({
  projectId,
  project,
  attention,
  onOpenOrder,
  onOpenProjectPage,
}: {
  projectId: string;
  /** The project from the shell's list; undefined while it loads. */
  project: Project | undefined;
  attention: { items: AttentionItem[]; running: RunningWorkOrder[] };
  onOpenOrder: (projectId: string, featureId: string) => void;
  /** The project's own page, until its pages open in the centre (D6). */
  onOpenProjectPage: (projectId: string) => void;
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
  const openPage = () => onOpenProjectPage(projectId);
  // The project page opens on its overview; the vision is asked for once it
  // has mounted and listens.
  const openVision = () => {
    onOpenProjectPage(projectId);
    setTimeout(() => window.dispatchEvent(new Event(OPEN_VISION)), 0);
  };
  const hasRepo = !!project?.forgejo_repository;

  return (
    <>
      <ProjectCard data={data} onOpenVision={openVision} />
      <WorkOrdersCard
        features={data.features}
        items={items}
        running={running}
        maxRunning={data.maxRunning}
        onOpen={(id) => onOpenOrder(projectId, id)}
      />
      <section className="shell-card">
        <h2 className="shell-card__head">Agents</h2>
        <p className="muted shell-card__text">
          The agents at work in the project. For now, each work order&apos;s page shows its own.
        </p>
      </section>
      {hasRepo && (
        <PreviewCard
          projectId={projectId}
          appName={project?.app ? project.name : undefined}
          toolchain={data.toolchain}
          onOpenPage={openPage}
        />
      )}
      {hasRepo && (
        <section className="shell-card">
          <h2 className="shell-card__head">Sync</h2>
          <CompactProjectSync projectId={projectId} onOpenFull={openPage} />
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

  return { features, vision, toolchain, maxRunning };
}

function ProjectCard({ data, onOpenVision }: { data: ProjectData; onOpenVision: () => void }) {
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
      <button className="shell-card__link" onClick={onOpenVision}>
        {vision === null ? "Plan it" : "Open the vision"}
      </button>
    </section>
  );
}

function WorkOrdersCard({
  features,
  items,
  running,
  maxRunning,
  onOpen,
}: {
  features: Feature[] | null;
  items: AttentionItem[];
  running: RunningWorkOrder[];
  maxRunning: number;
  onOpen: (featureId: string) => void;
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
