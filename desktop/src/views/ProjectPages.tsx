import { useCallback, useEffect, useState } from "react";
import { getProject } from "../api/projects";
import { createSession, listFeatures } from "../api/features";
import { getProjectToolchain } from "../api/toolchains";
import { getOpenVisionSession, getVision } from "../api/vision";
import { ApiError } from "../api/client";
import { pruneSessionPreviews } from "../ipc";
import { WORK } from "../vocab";
import type { AttentionItem, Project, ProjectToolchain, WorkKind } from "../api/types";
import { AttachmentOwner } from "./Attachments";
import { OPEN_VISION, WORK_CHANGED } from "./ChatView";
import { OPEN_PLAN } from "./chatEvents";
import { PlanPage } from "./PlanPage";
import { FeatureView } from "./FeatureView";
import { NewWorkOrder, type OrderDraft } from "./NewWorkOrder";
import { PreviewView } from "./PreviewView";
import { ProjectSettings } from "./ProjectSettings";
import { ProjectSyncCard } from "./ProjectSyncCard";
import { RepositoryCard } from "./RepositoryCard";
import { StackPicker } from "./StackPicker";
import { VisionView } from "./VisionView";
import { WorkOrderRail } from "./WorkOrderRail";
import { autopilotOn, shownMode, type PageMode, type PageTarget } from "./pageTarget";
import { usePreview } from "./usePreview";
import { usePreviewSetup } from "./usePreviewSetup";
import { requireWorkSuggestion } from "./useWorkSuggestion";
import { rememberedIdea } from "./visionSession";

export type ProjectPagesState = ReturnType<typeof useProjectPages>;

/** What a project's pages share, wherever they show: the project, its
 * stack, the preview and its setup, and where the pages point. The caller
 * keeps the page target, so the project page and the chat shell can each
 * decide what "home" is. */
export function useProjectPages(
  id: string,
  {
    page,
    setPage,
    onLoaded,
    openOnLoad = false,
  }: {
    page: PageTarget | null;
    setPage: (page: PageTarget | null) => void;
    onLoaded?: (name: string) => void;
    /** A project just created from an idea, or being planned, opens on its vision. */
    openOnLoad?: boolean;
  },
) {
  const [project, setProject] = useState<Project | null>(null);
  const [toolchain, setToolchain] = useState<ProjectToolchain | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState(0);
  // The rail lists one kind of work at a time: work orders or sessions.
  const [railKind, setRailKind] = useState<WorkKind>("work_order");
  // An app's preview opens in its own window, not the browser.
  const preview = usePreview(id, undefined, project?.app ? project.name : undefined);
  // Follows the background preview setup even when the Preview page is closed.
  const previewSetup = usePreviewSetup(id, preview);
  const [orderDraft, setOrderDraft] = useState<OrderDraft>();

  // A session's preview goes away once the session merges or is discarded.
  useEffect(() => {
    const prune = () =>
      listFeatures(id)
        .then((features) =>
          pruneSessionPreviews(
            id,
            features
              .filter(
                (f) => f.kind === "session" && f.state !== "completed" && f.state !== "cancelled",
              )
              .map((f) => f.id),
          ),
        )
        .catch(() => undefined);
    void prune();
    const timer = setInterval(() => void prune(), 60000);
    return () => clearInterval(timer);
  }, [id, reloadKey]);

  // Stable so it doesn't re-trigger FeatureView's load effect; refreshes the
  // work lists when a feature's state changes.
  const bump = useCallback(() => setReloadKey((k) => k + 1), []);
  // Work the assistant created or started shows up at once.
  useEffect(() => {
    window.addEventListener(WORK_CHANGED, bump);
    return () => window.removeEventListener(WORK_CHANGED, bump);
  }, [bump]);
  useEffect(() => {
    const showVision = () => setPage({ mode: "vision" });
    const showPlan = () => setPage({ mode: "plan" });
    window.addEventListener(OPEN_VISION, showVision);
    window.addEventListener(OPEN_PLAN, showPlan);
    return () => {
      window.removeEventListener(OPEN_VISION, showVision);
      window.removeEventListener(OPEN_PLAN, showPlan);
    };
  }, [setPage]);

  const load = useCallback(async () => {
    setError(null);
    try {
      const p = await getProject(id);
      setProject(p);
      onLoaded?.(p.name);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    }
    // Toolchain is a separate record; absent on older coordinators (404/other) —
    // leave it null and treat the project as unconstrained (no creation gate).
    try {
      const t = await getProjectToolchain(id);
      setToolchain(t);
      // A project just created from an idea opens on its vision conversation.
      if (openOnLoad && t.status === "needs_setup") {
        const open =
          !!rememberedIdea(id) ||
          (await getOpenVisionSession(id).then(
            () => true,
            () => false,
          ));
        if (open) setPage({ mode: "vision" });
      }
    } catch {
      setToolchain(null);
    }
    // A project being planned opens on its vision, which shows the planning
    // in progress and opens the plan and its insights.
    if (openOnLoad) {
      getVision(id)
        .then((vision) => {
          if (vision.planning?.status === "planning") setPage({ mode: "vision" });
        })
        .catch(() => undefined);
    }
  }, [id, onLoaded, openOnLoad, setPage]);

  useEffect(() => {
    setProject(null);
    setToolchain(null);
    void load();
  }, [load]);

  const hasRepo = project ? !!project.forgejo_repository : undefined;
  // Only gate when we actually have a toolchain record saying needs_setup.
  const needsStack = toolchain?.status === "needs_setup";
  const show = (mode: PageMode) => setPage({ mode });
  const openOrder = (orderId: string) => setPage({ mode: "order", orderId });
  // "New work order" goes to stack setup first while the toolchain isn't ready.
  const startNewOrder = () => {
    setOrderDraft(undefined);
    show("new");
  };
  // A session starts at once and opens on its greeting (ADR-019).
  const [startingSession, setStartingSession] = useState(false);
  const startSession = async () => {
    if (needsStack) {
      show("stack");
      return;
    }
    setStartingSession(true);
    setError(null);
    try {
      // The suggested agent: the freshest one, which the user can switch.
      const session = await createSession(id, await requireWorkSuggestion());
      bump();
      setRailKind("session");
      openOrder(session.id);
    } catch (e) {
      setError(
        e instanceof ApiError
          ? `${e.message} (${e.code})`
          : e instanceof Error
            ? e.message
            : String(e),
      );
    } finally {
      setStartingSession(false);
    }
  };

  return {
    id,
    project,
    setProject,
    toolchain,
    setToolchain,
    error,
    reloadKey,
    bump,
    railKind,
    setRailKind,
    preview,
    previewSetup,
    orderDraft,
    hasRepo,
    needsStack,
    /** The page on screen, or null for home. */
    mode: shownMode(page, needsStack),
    orderId: page?.orderId ?? null,
    setPage,
    show,
    openOrder,
    startNewOrder,
    startSession,
    startingSession,
  };
}

/** The page a project's pages point at; nothing for home, which the caller
 * draws. */
export function ProjectPageBody({
  pages,
  attention,
  onLoaded,
}: {
  pages: ProjectPagesState;
  /** This project's slice of the shared coordinator attention snapshot. */
  attention: AttentionItem[];
  onLoaded?: (name: string) => void;
}) {
  const { id, project, mode, orderId, needsStack, hasRepo } = pages;
  const home = () => pages.setPage(null);
  if (!project && mode !== "order") return null;

  switch (mode) {
    case "vision":
      return (
        <VisionView
          projectId={id}
          setup={
            needsStack
              ? {
                  onStarted: (fid, t) => {
                    pages.setToolchain(t);
                    pages.bump();
                    pages.setRailKind("work_order");
                    pages.openOrder(fid);
                  },
                }
              : undefined
          }
          onOpenOrder={(fid) => {
            pages.bump();
            pages.setRailKind("work_order");
            pages.openOrder(fid);
          }}
        />
      );
    case "plan":
      return <PlanPage projectId={id} onBack={home} />;
    case "repository":
      return <RepositoryCard projectId={id} />;
    case "stack":
      return (
        <StackPicker
          projectId={id}
          onSaved={(t) => {
            pages.setToolchain(t);
            home(); // return to where the flow was most likely initiated
          }}
        />
      );
    case "new":
      return (
        <NewWorkOrder
          key={pages.orderDraft?.title ?? "blank"}
          project={project!}
          initial={pages.orderDraft}
          onOpenValidation={() => pages.show("settings")}
          onCreated={(fid) => {
            pages.bump();
            pages.openOrder(fid);
          }}
        />
      );
    case "order":
      return (
        orderId && (
          <AttachmentOwner.Provider value={id}>
            <FeatureView
              projectId={id}
              featureId={orderId}
              hasRepo={hasRepo}
              autopilot={autopilotOn(attention, orderId)}
              onOpenValidation={() => pages.show("settings")}
              onChanged={pages.bump}
              onKind={pages.setRailKind}
              onDeleted={() => {
                home();
                pages.bump();
              }}
            />
          </AttachmentOwner.Provider>
        )
      );
    case "preview":
      return (
        <PreviewView
          projectId={id}
          preview={pages.preview}
          toolchain={pages.toolchain}
          hasRepo={!!project!.forgejo_repository}
          onToolchainSaved={pages.setToolchain}
          onOpenStack={() => pages.show("stack")}
          onOpenOrder={pages.openOrder}
          setup={pages.previewSetup}
        />
      );
    case "settings":
      return (
        <ProjectSettings
          project={project!}
          onUpdated={(updated) => {
            pages.setProject(updated);
            onLoaded?.(updated.name);
          }}
        />
      );
    case "sync":
      return <ProjectSyncCard projectId={id} projectName={project!.name} />;
    case "work":
      return <AllWork pages={pages} />;
    default:
      return null;
  }
}

// Every work order and session of the project, done and closed ones
// included, as the project page's rail listed them.
function AllWork({ pages }: { pages: ProjectPagesState }) {
  const sessions = pages.railKind === "session";
  return (
    <section className="panel">
      <div className="panel__head">
        <span className="rail__tabs" role="tablist">
          <button
            role="tab"
            aria-selected={!sessions}
            className={`rail__tab ${!sessions ? "rail__tab--on" : ""}`}
            onClick={() => pages.setRailKind("work_order")}
          >
            {WORK.Plural}
          </button>
          {pages.hasRepo && (
            <button
              role="tab"
              aria-selected={sessions}
              className={`rail__tab ${sessions ? "rail__tab--on" : ""}`}
              onClick={() => pages.setRailKind("session")}
            >
              Sessions
            </button>
          )}
        </span>
        {sessions ? (
          <button
            className="ghost"
            title={pages.needsStack ? "Choose a stack first" : "Start a session with an agent"}
            onClick={() => void pages.startSession()}
            disabled={pages.startingSession}
          >
            {pages.startingSession ? "Starting…" : "+ New session"}
          </button>
        ) : (
          <button
            className="ghost"
            title={pages.needsStack ? "Choose a stack first" : WORK.newAction}
            onClick={pages.startNewOrder}
          >
            + {WORK.newAction}
          </button>
        )}
      </div>
      <WorkOrderRail
        kind={pages.railKind}
        projectId={pages.id}
        selectedId={null}
        reloadKey={pages.reloadKey}
        onSelect={pages.openOrder}
      />
    </section>
  );
}
