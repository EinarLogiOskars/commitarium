import { useEffect, useState } from "react";
import { createFeature } from "../api/features";
import { ApiError } from "../api/client";
import { AgentModelFields } from "./AgentModelFields";
import { useModels, pickModel } from "./useModels";
import { WORK } from "../vocab";
import type { AgentModels, AgentProviders, AutonomyPolicy, MergePolicy, Project } from "../api/types";

/** Create-a-work-order form. Settings default to the project's, and can be
 * changed for this one order (backend applies the effective values). */
export function NewWorkOrder({
  project,
  onCreated,
}: {
  project: Project;
  onCreated: (featureId: string) => void;
}) {
  const [title, setTitle] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Per-order settings, seeded from the project defaults.
  const [showOptions, setShowOptions] = useState(false);
  const { modelsFor, loading: modelsLoading } = useModels();
  const [providers, setProviders] = useState<AgentProviders>({
    lead: project.agent_providers?.lead ?? "codex",
    reviewer: project.agent_providers?.reviewer ?? "codex",
  });
  const [models, setModels] = useState<AgentModels>({
    lead: project.agent_models?.lead ?? "",
    reviewer: project.agent_models?.reviewer ?? "",
  });
  const [autonomy, setAutonomy] = useState<AutonomyPolicy>(project.autonomy_policy ?? "review_each_phase");
  const [merge, setMerge] = useState<MergePolicy>(project.merge_policy ?? "require_user_approval");
  const [planning, setPlanning] = useState(project.dialogue_limits?.planning_rounds ?? 6);
  const [review, setReview] = useState(project.dialogue_limits?.implementation_review_rounds ?? 6);

  useEffect(() => {
    if (modelsLoading) return;
    setModels((m) => ({
      lead: pickModel(modelsFor(providers.lead, "lead"), m.lead),
      reviewer: pickModel(modelsFor(providers.reviewer, "reviewer"), m.reviewer),
    }));
  }, [modelsLoading, modelsFor, providers.lead, providers.reviewer]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!title.trim()) return;
    setBusy(true);
    setError(null);
    try {
      const created = await createFeature(project.id, {
        title: title.trim(),
        description: description.trim(),
        agent_providers: providers,
        // Only override models when both are resolved from the catalog; otherwise
        // omit so the backend applies the project's model defaults.
        ...(models.lead && models.reviewer ? { agent_models: models } : {}),
        autonomy_policy: autonomy,
        merge_policy: merge,
        dialogue_limits: { planning_rounds: planning, implementation_review_rounds: review },
      });
      onCreated(created.id);
    } catch (e) {
      if (e instanceof ApiError && e.code === "project_toolchain_required") {
        setError("This project needs a stack before you can create work orders. Open Stack to choose one.");
      } else {
        setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
      }
    } finally {
      setBusy(false);
    }
  };

  const summary = [
    `${cap(providers.lead)} lead · ${cap(providers.reviewer)} reviewer`,
    autonomy === "run_to_completion" ? "runs to merge gate" : "stops each phase",
    merge === "auto_after_gates" ? "auto-merge" : "approval to merge",
    `${planning}/${review} rounds`,
  ].join(" · ");

  return (
    <section className="panel">
      <h2>{WORK.newAction}</h2>
      {error && <div className="banner banner--error">{error}</div>}
      <form className="create create--feature" onSubmit={submit}>
        <input
          type="text"
          placeholder={`${WORK.Singular} title`}
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          disabled={busy}
          autoFocus
        />
        <textarea
          placeholder="Description (optional)"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
          disabled={busy}
          rows={3}
        />

        <div className="neworder__options">
          <button
            type="button"
            className="neworder__options-head"
            onClick={() => setShowOptions((v) => !v)}
          >
            <span className="activity-group__chevron">{showOptions ? "▼" : "▶"}</span>
            Options for this order
            {!showOptions && <span className="neworder__summary">{summary}</span>}
          </button>

          {showOptions && (
            <div className="neworder__grid">
              <AgentModelFields
                providers={providers}
                models={models}
                modelsFor={modelsFor}
                onChange={(p, m) => {
                  setProviders(p);
                  setModels(m);
                }}
                disabled={busy}
              />
              <label>
                Autonomy
                <select value={autonomy} onChange={(e) => setAutonomy(e.target.value as AutonomyPolicy)} disabled={busy}>
                  <option value="review_each_phase">Stop at each phase</option>
                  <option value="run_to_completion">Run to the merge gate</option>
                </select>
              </label>
              <label>
                Merge
                <select value={merge} onChange={(e) => setMerge(e.target.value as MergePolicy)} disabled={busy}>
                  <option value="require_user_approval">Require my approval</option>
                  <option value="auto_after_gates">Auto after gates</option>
                </select>
              </label>
              <label>
                Planning rounds
                <input
                  type="number"
                  min={0}
                  value={planning}
                  onChange={(e) => setPlanning(Math.max(0, Number(e.target.value)))}
                  disabled={busy}
                />
              </label>
              <label>
                Review rounds
                <input
                  type="number"
                  min={0}
                  value={review}
                  onChange={(e) => setReview(Math.max(0, Number(e.target.value)))}
                  disabled={busy}
                />
              </label>
            </div>
          )}
        </div>

        <button className="primary" type="submit" disabled={busy || !title.trim()}>
          {busy ? "Creating…" : "Create"}
        </button>
      </form>
    </section>
  );
}

function cap(s: string): string {
  return s.charAt(0).toUpperCase() + s.slice(1);
}
