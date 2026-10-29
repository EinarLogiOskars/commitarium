import { useEffect, useState } from "react";
import { WorkOrderRail } from "./WorkOrderRail";
import { ProjectDashboard } from "./ProjectDashboard";
import { PreviewRailItem } from "./PreviewRailItem";
import { ProjectPageBody, useProjectPages } from "./ProjectPages";
import { PlanOverlay } from "./PlanPage";
import type { PageMode, PageTarget } from "./pageTarget";
import { WORK } from "../vocab";
import type { AttentionItem } from "../api/types";

type Mode = "overview" | PageMode;

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
  const [page, setPage] = useState<PageTarget | null>(null);
  const pages = useProjectPages(id, { page, setPage, onLoaded, openOnLoad: true });
  const { project, toolchain, error, needsStack, hasRepo, railKind, setRailKind } = pages;
  const mode: Mode = pages.mode ?? "overview";
  const orderId = pages.orderId;

  // Where the workspace points. Switching project lands on the overview; an
  // inbox deep link opens its work order instead — including when the click
  // switches project and selects an order in the same step. The target's nonce
  // makes re-opening the same order after navigating away work.
  useEffect(() => {
    setPage(openOrderTarget ? { mode: "order", orderId: openOrderTarget.featureId } : null);
  }, [id, openOrderTarget]);

  useEffect(() => {
    onForeground?.(mode === "order" ? orderId : null);
  }, [mode, orderId, onForeground]);

  const go = (next: Mode) => setPage(next === "overview" ? null : { mode: next });
  const openOrder = pages.openOrder;

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
              onClick={() => go(page.mode)}
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
            onClick={() => go("overview")}
          >
            Overview
          </button>
          {project?.forgejo_repository && (
            <button
              className={`rail__item ${mode === "vision" ? "rail__item--active" : ""}`}
              onClick={() => go("vision")}
            >
              Vision
            </button>
          )}
          {project?.forgejo_repository && (
            <button
              className={`rail__item ${mode === "repository" ? "rail__item--active" : ""}`}
              onClick={() => go("repository")}
            >
              Repository
            </button>
          )}
          {toolchain && (
            <button
              className={`rail__item ${mode === "stack" ? "rail__item--active" : ""}`}
              onClick={() => go("stack")}
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
                onClick={() => void pages.startSession()}
                disabled={pages.startingSession}
              >
                {pages.startingSession ? "Starting…" : "+ New"}
              </button>
            ) : (
              <button
                className="rail__new"
                title={needsStack ? "Choose a stack first" : WORK.newAction}
                onClick={pages.startNewOrder}
              >
                + New
              </button>
            )}
          </div>
          <WorkOrderRail
            kind={railKind}
            projectId={id}
            selectedId={mode === "order" ? orderId : null}
            reloadKey={pages.reloadKey}
            onSelect={openOrder}
          />

          <div className="rail__spacer" />
          {project?.forgejo_repository && (
            <PreviewRailItem
              preview={pages.preview}
              runnable={!!toolchain?.run}
              active={mode === "preview"}
              onOpenPage={() => go("preview")}
            />
          )}
          <button
            className={`rail__item ${mode === "settings" ? "rail__item--active" : ""}`}
            onClick={() => go("settings")}
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
              onOpenStack={() => go("stack")}
              onOpenVision={() => go("vision")}
              attention={attention}
              onOpenOrder={openOrder}
              onNewOrder={pages.startNewOrder}
              onSettings={() => go("settings")}
            />
          )}

          {mode !== "overview" && (
            <ProjectPageBody pages={pages} attention={attention} onLoaded={onLoaded} />
          )}
          {/* The chat's "Open the plan" opens the written plan over the page. */}
          <PlanOverlay projectId={id} />
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
