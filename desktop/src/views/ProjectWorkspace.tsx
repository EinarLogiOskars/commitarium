import { AttachmentOwner } from "./Attachments";
import { useCallback, useEffect, useState } from "react";
import { getProject } from "../api/projects";
import { createSession, listFeatures } from "../api/features";
import { requireWorkSuggestion } from "./useWorkSuggestion";
import { pruneSessionPreviews } from "../ipc";
import { getProjectToolchain } from "../api/toolchains";
import { ApiError } from "../api/client";
import { FeatureView } from "./FeatureView";
import { WorkOrderRail } from "./WorkOrderRail";
import { NewWorkOrder, type OrderDraft } from "./NewWorkOrder";
import { ProjectSettings } from "./ProjectSettings";
import { ProjectDashboard } from "./ProjectDashboard";
import { RepositoryCard } from "./RepositoryCard";
import { StackPicker } from "./StackPicker";
import { PreviewRailItem } from "./PreviewRailItem";
import { PreviewView } from "./PreviewView";
import { VisionView } from "./VisionView";
import { OPEN_VISION, WORK_CHANGED } from "./AssistantDrawer";
import { rememberedIdea } from "./visionSession";
import { getOpenVisionSession, getVision } from "../api/vision";
import { usePreview } from "./usePreview";
import { usePreviewSetup } from "./usePreviewSetup";
import { WORK } from "../vocab";
import type { AttentionItem, Project, ProjectToolchain, WorkKind } from "../api/types";

type Mode =
  "overview" | "vision" | "repository" | "stack" | "new" | "order" | "settings" | "preview";

/** Project workspace shell: work-order rail + master/detail main pane. */
export function ProjectWorkspace({
  id,
  onLoaded,
  attention,
  openOrder: openOrderTarget,
  onForeground,
  railHeader,
  systemNav,
  railCollapsed = false,
  onToggleRail,
  railStrip,
  autopilot,
  onExit,
  exitLabel = "Projects",
}: {
  id: string;
  onLoaded?: (name: string) => void;
  /** This project's slice of the shared coordinator attention snapshot. */
  attention: AttentionItem[];
  /** A work order the inbox asked for; the nonce re-triggers a repeat open. */
  openOrder: { featureId: string; nonce: number } | null;
  /** Reports the work order on screen, so its own news isn't also notified. */
  onForeground?: (featureId: string | null) => void;
  /** The project switcher, at the top of the rail. */
  railHeader?: React.ReactNode;
  /** The app's own controls, at the bottom of the rail. */
  systemNav?: React.ReactNode;
  /** The rail folded to a strip with only its expand button. */
  railCollapsed?: boolean;
  onToggleRail?: () => void;
  /** The app's controls kept on the collapsed strip. */
  railStrip?: React.ReactNode;
  /** The autopilot banner while it is on. */
  autopilot?: React.ReactNode;
  /** Back to the project list or the apps; the collapsed rail's top icon. */
  onExit?: () => void;
  exitLabel?: string;
}) {
  const [project, setProject] = useState<Project | null>(null);
  const [toolchain, setToolchain] = useState<ProjectToolchain | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [mode, setMode] = useState<Mode>("overview");
  const [orderId, setOrderId] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState(0);
  // The rail lists one kind of work at a time: work orders or sessions.
  const [railKind, setRailKind] = useState<WorkKind>("work_order");
  // An app's preview opens in its own window, not the browser.
  const preview = usePreview(id, undefined, project?.app ? project.name : undefined);
  // Follows the background preview setup even when the Preview page is closed.
  const previewSetup = usePreviewSetup(id, preview);

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
  const [orderDraft, setOrderDraft] = useState<OrderDraft>();
  // Stable so it doesn't re-trigger FeatureView's load effect; refreshes the
  // rail's work-order grouping when a feature's state changes.
  const bumpRail = useCallback(() => setReloadKey((k) => k + 1), []);
  // Work the assistant created or started shows up at once.
  useEffect(() => {
    window.addEventListener(WORK_CHANGED, bumpRail);
    return () => window.removeEventListener(WORK_CHANGED, bumpRail);
  }, [bumpRail]);
  useEffect(() => {
    const showVision = () => {
      setMode("vision");
      setOrderId(null);
    };
    window.addEventListener(OPEN_VISION, showVision);
    return () => window.removeEventListener(OPEN_VISION, showVision);
  }, []);

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
      if (t.status === "needs_setup") {
        const open =
          !!rememberedIdea(id) ||
          (await getOpenVisionSession(id).then(
            () => true,
            () => false,
          ));
        if (open) setMode("vision");
      }
    } catch {
      setToolchain(null);
    }
    // A project being planned opens on its planning workspace.
    getVision(id)
      .then((vision) => {
        if (vision.planning?.status === "planning") setMode("vision");
      })
      .catch(() => undefined);
  }, [id, onLoaded]);

  useEffect(() => {
    setProject(null);
    setToolchain(null);
    void load();
  }, [load]);

  // Where the workspace points. Switching project lands on the overview; an
  // inbox deep link opens its work order instead — including when the click
  // switches project and selects an order in the same step. The target's nonce
  // makes re-opening the same order after navigating away work.
  useEffect(() => {
    setOrderId(openOrderTarget?.featureId ?? null);
    setMode(openOrderTarget ? "order" : "overview");
  }, [id, openOrderTarget]);

  const openOrder = (featureId: string) => {
    setOrderId(featureId);
    setMode("order");
  };

  useEffect(() => {
    onForeground?.(mode === "order" ? orderId : null);
  }, [mode, orderId, onForeground]);

  const hasRepo = project ? !!project.forgejo_repository : undefined;
  // Only gate when we actually have a toolchain record saying needs_setup.
  const needsStack = toolchain?.status === "needs_setup";
  // Route "new work order" to stack setup first when the toolchain isn't ready.
  const startNewOrder = () => {
    setOrderDraft(undefined);
    setOrderId(null);
    setMode(needsStack ? "stack" : "new");
  };
  // A session starts at once and opens on its greeting (ADR-019).
  const [startingSession, setStartingSession] = useState(false);
  const startSession = async () => {
    if (needsStack) {
      setOrderId(null);
      setMode("stack");
      return;
    }
    setStartingSession(true);
    setError(null);
    try {
      // The suggested agent: the freshest one, which the user can switch.
      const session = await createSession(id, await requireWorkSuggestion());
      setReloadKey((k) => k + 1);
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

  // The pages the collapsed rail keeps, as icons; work orders and sessions
  // stay in the full rail.
  const repo = !!project?.forgejo_repository;
  const stripPages: { mode: Mode; label: string }[] = [
    { mode: "overview", label: "Overview" },
    ...(repo ? [{ mode: "vision" as Mode, label: "Vision" }] : []),
    ...(repo ? [{ mode: "repository" as Mode, label: "Repository" }] : []),
    ...(toolchain ? [{ mode: "stack" as Mode, label: "Stack" }] : []),
    ...(repo ? [{ mode: "preview" as Mode, label: "Preview" }] : []),
    { mode: "settings", label: "Project settings" },
  ];

  return (
    <>
      {railCollapsed ? (
        <nav className="rail rail--collapsed">
          {onExit && (
            <button
              className="ghost rail__icon"
              onClick={onExit}
              data-tip={`Back to ${exitLabel}`}
              aria-label={`Back to ${exitLabel}`}
            >
              ←
            </button>
          )}
          <RailToggle collapsed onToggle={onToggleRail} />
          {stripPages.map((page) => (
            <button
              key={page.mode}
              className={`ghost rail__icon ${mode === page.mode ? "rail__icon--active" : ""}`}
              onClick={() => {
                setMode(page.mode);
                setOrderId(null);
              }}
              data-tip={page.label}
              aria-label={page.label}
            >
              <RailIcon name={page.mode} />
              {page.mode === "stack" && needsStack && (
                <span className="rail__icon-dot" aria-label="Needs setting up" />
              )}
            </button>
          ))}
          <div className="rail__spacer" />
          {railStrip}
        </nav>
      ) : (
        <nav className="rail">
          <div className="rail__top">
            {railHeader}
            {onToggleRail && <RailToggle collapsed={false} onToggle={onToggleRail} />}
          </div>
          <button
            className={`rail__item ${mode === "overview" ? "rail__item--active" : ""}`}
            onClick={() => {
              setMode("overview");
              setOrderId(null);
            }}
          >
            Overview
          </button>
          {project?.forgejo_repository && (
            <button
              className={`rail__item ${mode === "vision" ? "rail__item--active" : ""}`}
              onClick={() => {
                setMode("vision");
                setOrderId(null);
              }}
            >
              Vision
            </button>
          )}
          {project?.forgejo_repository && (
            <button
              className={`rail__item ${mode === "repository" ? "rail__item--active" : ""}`}
              onClick={() => {
                setMode("repository");
                setOrderId(null);
              }}
            >
              Repository
            </button>
          )}
          {toolchain && (
            <button
              className={`rail__item ${mode === "stack" ? "rail__item--active" : ""}`}
              onClick={() => {
                setMode("stack");
                setOrderId(null);
              }}
            >
              Stack
              {needsStack && <span className="pill pill--warn rail__badge">Set up</span>}
            </button>
          )}

          <div className="rail__section-head">
            <span className="rail__tabs" role="tablist">
              <button
                role="tab"
                aria-selected={railKind === "work_order"}
                className={`rail__tab ${railKind === "work_order" ? "rail__tab--on" : ""}`}
                onClick={() => setRailKind("work_order")}
              >
                {WORK.Plural}
              </button>
              {hasRepo && (
                <button
                  role="tab"
                  aria-selected={railKind === "session"}
                  className={`rail__tab ${railKind === "session" ? "rail__tab--on" : ""}`}
                  onClick={() => setRailKind("session")}
                >
                  Sessions
                </button>
              )}
            </span>
            {railKind === "session" ? (
              <button
                className="rail__new"
                title={needsStack ? "Choose a stack first" : "Start a session with an agent"}
                onClick={() => void startSession()}
                disabled={startingSession}
              >
                {startingSession ? "Starting…" : "+ New"}
              </button>
            ) : (
              <button
                className="rail__new"
                title={needsStack ? "Choose a stack first" : WORK.newAction}
                onClick={startNewOrder}
              >
                + New
              </button>
            )}
          </div>
          <WorkOrderRail
            kind={railKind}
            projectId={id}
            selectedId={mode === "order" ? orderId : null}
            reloadKey={reloadKey}
            onSelect={openOrder}
          />

          <div className="rail__spacer" />
          {project?.forgejo_repository && (
            <PreviewRailItem
              preview={preview}
              runnable={!!toolchain?.run}
              active={mode === "preview"}
              onOpenPage={() => {
                setMode("preview");
                setOrderId(null);
              }}
            />
          )}
          <button
            className={`rail__item ${mode === "settings" ? "rail__item--active" : ""}`}
            onClick={() => {
              setMode("settings");
              setOrderId(null);
            }}
          >
            Project settings
          </button>
          {systemNav && <div className="rail__system">{systemNav}</div>}
        </nav>
      )}

      <main className="main">
        <div className={`main__inner ${mode === "order" ? "main__inner--fill" : ""}`}>
          {autopilot}
          {error && <div className="banner banner--error">{error}</div>}

          {mode === "overview" && project && (
            <ProjectDashboard
              project={project}
              needsStack={needsStack}
              onOpenStack={() => {
                setMode("stack");
                setOrderId(null);
              }}
              onOpenVision={() => {
                setMode("vision");
                setOrderId(null);
              }}
              attention={attention}
              onOpenOrder={openOrder}
              onNewOrder={startNewOrder}
              onSettings={() => {
                setMode("settings");
                setOrderId(null);
              }}
            />
          )}

          {mode === "vision" && project && (
            <VisionView
              projectId={id}
              setup={
                needsStack
                  ? {
                      onStarted: (fid, t) => {
                        setToolchain(t);
                        setReloadKey((k) => k + 1);
                        setRailKind("work_order");
                        openOrder(fid);
                      },
                    }
                  : undefined
              }
              onOpenOrder={(fid) => {
                setReloadKey((k) => k + 1);
                setRailKind("work_order");
                openOrder(fid);
              }}
            />
          )}

          {mode === "repository" && project && <RepositoryCard projectId={id} />}

          {mode === "stack" && project && (
            <StackPicker
              projectId={id}
              onSaved={(t) => {
                setToolchain(t);
                setMode("overview"); // return to where the flow was most likely initiated
              }}
            />
          )}

          {mode === "new" && project && (
            <NewWorkOrder
              key={orderDraft?.title ?? "blank"}
              project={project}
              initial={orderDraft}
              onOpenValidation={() => {
                setOrderId(null);
                setMode("settings");
              }}
              onCreated={(fid) => {
                setReloadKey((k) => k + 1);
                openOrder(fid);
              }}
            />
          )}

          {mode === "order" && orderId && (
            <AttachmentOwner.Provider value={id}>
              <FeatureView
                projectId={id}
                featureId={orderId}
                hasRepo={hasRepo}
                autopilot={autopilotOn(attention, orderId)}
                onOpenValidation={() => setMode("settings")}
                onChanged={bumpRail}
                onKind={setRailKind}
                onDeleted={() => {
                  setMode("overview");
                  setOrderId(null);
                  bumpRail();
                }}
              />
            </AttachmentOwner.Provider>
          )}

          {mode === "preview" && project && (
            <PreviewView
              projectId={id}
              preview={preview}
              toolchain={toolchain}
              hasRepo={!!project.forgejo_repository}
              onToolchainSaved={setToolchain}
              onOpenStack={() => setMode("stack")}
              onOpenOrder={openOrder}
              setup={previewSetup}
            />
          )}

          {mode === "settings" && project && (
            <ProjectSettings
              project={project}
              onUpdated={(updated) => {
                setProject(updated);
                onLoaded?.(updated.name);
              }}
            />
          )}
        </div>
      </main>
    </>
  );
}

function RailToggle({ collapsed, onToggle }: { collapsed: boolean; onToggle?: () => void }) {
  return (
    <button
      className="ghost rail__toggle"
      onClick={onToggle}
      title={collapsed ? undefined : "Hide the sidebar"}
      data-tip={collapsed ? "Show the sidebar" : undefined}
      aria-label={collapsed ? "Show the sidebar" : "Hide the sidebar"}
      aria-expanded={!collapsed}
    >
      <svg viewBox="0 0 24 24" width="18" height="18" fill="none" aria-hidden>
        <rect
          x="3.5"
          y="4.5"
          width="17"
          height="15"
          rx="2.5"
          stroke="currentColor"
          strokeWidth="1.6"
        />
        <path d="M9.5 4.5v15" stroke="currentColor" strokeWidth="1.6" />
      </svg>
    </button>
  );
}

// Outline icons for the collapsed rail, drawn on a 24px grid.
const RAIL_ICONS: Partial<Record<Mode, React.ReactNode>> = {
  overview: (
    <>
      <rect x="4" y="4" width="7" height="7" rx="1.5" />
      <rect x="13" y="4" width="7" height="7" rx="1.5" />
      <rect x="4" y="13" width="7" height="7" rx="1.5" />
      <rect x="13" y="13" width="7" height="7" rx="1.5" />
    </>
  ),
  vision: (
    <>
      <circle cx="12" cy="12" r="8" />
      <circle cx="12" cy="12" r="4.5" />
      <circle cx="12" cy="12" r="1" />
    </>
  ),
  repository: (
    <>
      <circle cx="7" cy="6" r="2" />
      <circle cx="7" cy="18" r="2" />
      <circle cx="17" cy="8" r="2" />
      <path d="M7 8v8M17 10c0 4-10 2-10 6" />
    </>
  ),
  stack: (
    <>
      <path d="M12 4l8 4-8 4-8-4 8-4z" />
      <path d="M4 12l8 4 8-4M4 16l8 4 8-4" />
    </>
  ),
  preview: <path d="M8 5.5v13l10.5-6.5L8 5.5z" />,
  settings: (
    <>
      <circle cx="12" cy="12" r="3" />
      <path d="M12 3.5v2.5M12 18v2.5M3.5 12H6M18 12h2.5M6 6l1.8 1.8M16.2 16.2L18 18M6 18l1.8-1.8M16.2 7.8L18 6" />
    </>
  ),
};

function RailIcon({ name }: { name: Mode }) {
  return (
    <svg
      viewBox="0 0 24 24"
      width="18"
      height="18"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      {RAIL_ICONS[name]}
    </svg>
  );
}

/** How autopilot stands on a blocked or failed work order, from the inbox. */
function autopilotOn(
  attention: AttentionItem[],
  featureId: string,
): "handling" | "stalled" | undefined {
  for (const item of attention) {
    if (item.feature_id !== featureId) continue;
    if (item.kind === "autopilot_stalled") return "stalled";
    if (item.kind === "autopilot_handling") return "handling";
  }
  return undefined;
}
