import { useCallback, useEffect, useState } from "react";
import { getProject } from "../api/projects";
import { createSession, listFeatures } from "../api/features";
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
}: {
  id: string;
  onLoaded?: (name: string) => void;
  /** This project's slice of the shared coordinator attention snapshot. */
  attention: AttentionItem[];
  /** A work order the inbox asked for; the nonce re-triggers a repeat open. */
  openOrder: { featureId: string; nonce: number } | null;
  /** Reports the work order on screen, so its own news isn't also notified. */
  onForeground?: (featureId: string | null) => void;
}) {
  const [project, setProject] = useState<Project | null>(null);
  const [toolchain, setToolchain] = useState<ProjectToolchain | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [mode, setMode] = useState<Mode>("overview");
  const [orderId, setOrderId] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState(0);
  // The rail lists one kind of work at a time: work orders or sessions.
  const [railKind, setRailKind] = useState<WorkKind>("work_order");
  const preview = usePreview(id);
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
      setToolchain(await getProjectToolchain(id));
    } catch {
      setToolchain(null);
    }
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
      const session = await createSession(id);
      setReloadKey((k) => k + 1);
      setRailKind("session");
      openOrder(session.id);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    } finally {
      setStartingSession(false);
    }
  };

  return (
    <>
      <nav className="rail">
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
          Settings
        </button>
      </nav>

      <main className="main">
        <div className={`main__inner ${mode === "order" ? "main__inner--fill" : ""}`}>
          {error && <div className="banner banner--error">{error}</div>}

          {mode === "overview" && project && (
            <ProjectDashboard
              project={project}
              needsStack={needsStack}
              onOpenStack={() => {
                setMode("stack");
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
              preferred={{
                agent: project.agent_providers?.lead_agent ?? project.agent_providers?.lead,
                model: project.agent_models?.lead,
              }}
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
            <FeatureView
              projectId={id}
              featureId={orderId}
              hasRepo={hasRepo}
              onOpenValidation={() => setMode("settings")}
              onChanged={bumpRail}
              onKind={setRailKind}
              onDeleted={() => {
                setMode("overview");
                setOrderId(null);
                bumpRail();
              }}
            />
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
            <ProjectSettings project={project} onUpdated={setProject} />
          )}
        </div>
      </main>
    </>
  );
}
