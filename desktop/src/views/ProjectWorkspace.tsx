import { useCallback, useEffect, useState } from "react";
import { getProject } from "../api/projects";
import { getProjectToolchain } from "../api/toolchains";
import { ApiError } from "../api/client";
import { FeatureView } from "./FeatureView";
import { WorkOrderRail } from "./WorkOrderRail";
import { NewWorkOrder } from "./NewWorkOrder";
import { ProjectSettings } from "./ProjectSettings";
import { ProjectDashboard } from "./ProjectDashboard";
import { RepositoryCard } from "./RepositoryCard";
import { StackPicker } from "./StackPicker";
import { WORK } from "../vocab";
import type { Project, ProjectToolchain } from "../api/types";

type Mode = "overview" | "repository" | "stack" | "new" | "order" | "settings";

/** Project workspace shell: work-order rail + master/detail main pane. */
export function ProjectWorkspace({
  id,
  onLoaded,
}: {
  id: string;
  onLoaded?: (name: string) => void;
}) {
  const [project, setProject] = useState<Project | null>(null);
  const [toolchain, setToolchain] = useState<ProjectToolchain | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [mode, setMode] = useState<Mode>("overview");
  const [orderId, setOrderId] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState(0);
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
    setMode("overview");
    setOrderId(null);
    void load();
  }, [load]);

  const openOrder = (featureId: string) => {
    setOrderId(featureId);
    setMode("order");
  };

  const hasRepo = project ? !!project.forgejo_repository : undefined;
  // Only gate when we actually have a toolchain record saying needs_setup.
  const needsStack = toolchain?.status === "needs_setup";
  // Route "new work order" to stack setup first when the toolchain isn't ready.
  const startNewOrder = () => {
    setOrderId(null);
    setMode(needsStack ? "stack" : "new");
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
          <span>{WORK.Plural}</span>
          <button
            className="rail__new"
            title={needsStack ? "Choose a stack first" : WORK.newAction}
            onClick={startNewOrder}
          >
            + New
          </button>
        </div>
        <WorkOrderRail
          projectId={id}
          selectedId={mode === "order" ? orderId : null}
          reloadKey={reloadKey}
          onSelect={openOrder}
        />

        <div className="rail__spacer" />
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
              onOpenOrder={openOrder}
              onNewOrder={startNewOrder}
              onSettings={() => {
                setMode("settings");
                setOrderId(null);
              }}
            />
          )}

          {mode === "repository" && project && <RepositoryCard projectId={id} />}

          {mode === "stack" && project && (
            <StackPicker
              projectId={id}
              onSaved={(t) => {
                setToolchain(t);
                setMode("new"); // configured now — go straight to creating an order
              }}
            />
          )}

          {mode === "new" && project && (
            <NewWorkOrder
              project={project}
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
              onChanged={bumpRail}
              onDeleted={() => {
                setMode("overview");
                setOrderId(null);
                bumpRail();
              }}
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
