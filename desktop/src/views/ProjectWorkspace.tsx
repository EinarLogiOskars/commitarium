import { useCallback, useEffect, useState } from "react";
import { getProject } from "../api/projects";
import { ApiError } from "../api/client";
import { FeatureView } from "./FeatureView";
import { WorkOrderRail } from "./WorkOrderRail";
import { NewWorkOrder } from "./NewWorkOrder";
import { ProjectSettings } from "./ProjectSettings";
import { ProjectDashboard } from "./ProjectDashboard";
import { WORK } from "../vocab";
import type { Project } from "../api/types";

type Mode = "overview" | "new" | "order" | "settings";

/** Project workspace shell: work-order rail + master/detail main pane. */
export function ProjectWorkspace({
  id,
  onExit,
  onLoaded,
}: {
  id: string;
  onExit: () => void;
  onLoaded?: (name: string) => void;
}) {
  const [project, setProject] = useState<Project | null>(null);
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
  }, [id, onLoaded]);

  useEffect(() => {
    setProject(null);
    setMode("overview");
    setOrderId(null);
    void load();
  }, [load]);

  const openOrder = (featureId: string) => {
    setOrderId(featureId);
    setMode("order");
  };

  const hasRepo = project ? !!project.forgejo_repository : undefined;

  return (
    <>
      <nav className="rail">
        <button className="rail__back" onClick={onExit}>‹ Projects</button>
        <WorkOrderRail
          projectId={id}
          selectedId={mode === "order" ? orderId : null}
          reloadKey={reloadKey}
          onSelect={openOrder}
        />
        <button
          className="rail__item"
          onClick={() => {
            setMode("new");
            setOrderId(null);
          }}
        >
          + {WORK.newAction}
        </button>
        <div className="rail__spacer" />
        <button
          className={`rail__item ${mode === "overview" ? "rail__item--active" : ""}`}
          onClick={() => {
            setMode("overview");
            setOrderId(null);
          }}
        >
          Project overview
        </button>
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
              onNewOrder={() => {
                setMode("new");
                setOrderId(null);
              }}
              onSettings={() => {
                setMode("settings");
                setOrderId(null);
              }}
            />
          )}

          {mode === "new" && (
            <NewWorkOrder
              projectId={id}
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
