import { useEffect, useState } from "react";
import { createFeature } from "../api/features";
import { getProjectValidation } from "../api/projects";
import { ApiError } from "../api/client";
import { NoValidationWarning } from "./NoValidationWarning";
import { AgentModelFields } from "./AgentModelFields";
import { useModels, pickModel } from "./useModels";
import { agentFor, agentName, useAgents } from "./useAgents";
import { WORK } from "../vocab";
import type {
  AgentModels,
  AgentProviders,
  AutonomyPolicy,
  MergePolicy,
  Project,
} from "../api/types";

/** Create-a-work-order form. Settings default to the project's, and can be
 * changed for this one order (backend applies the effective values). */
/** A prefilled work order, optionally with lighter settings than the project's. */
export interface OrderDraft {
  title: string;
  description: string;
  independentTests?: boolean;
}

export function NewWorkOrder({
  project,
  initial,
  onOpenValidation,
  onCreated,
}: {
  project: Project;
  /** Prefill for a work order started from elsewhere (e.g. preview setup). */
  initial?: OrderDraft;
  /** Opens the project settings where validation commands are configured. */
  onOpenValidation: () => void;
  onCreated: (featureId: string) => void;
}) {
  const [title, setTitle] = useState(initial?.title ?? "");
  const [description, setDescription] = useState(initial?.description ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Per-order settings, seeded from the project defaults.
  const [showOptions, setShowOptions] = useState(false);
  const { modelsFor, loading: modelsLoading } = useModels();
  const agents = useAgents();
  const [providers, setProviders] = useState<AgentProviders>({
    lead: project.agent_providers?.lead ?? "codex",
    reviewer: project.agent_providers?.reviewer ?? "codex",
    lead_agent: project.agent_providers?.lead_agent,
    reviewer_agent: project.agent_providers?.reviewer_agent,
  });
  const [models, setModels] = useState<AgentModels>({
    lead: project.agent_models?.lead ?? "",
    reviewer: project.agent_models?.reviewer ?? "",
  });
  const [autonomy, setAutonomy] = useState<AutonomyPolicy>(
    project.autonomy_policy ?? "review_each_phase",
  );
  const [merge, setMerge] = useState<MergePolicy>(project.merge_policy ?? "require_user_approval");
  const [independentTests, setIndependentTests] = useState(
    initial?.independentTests ?? project.independent_acceptance_tests ?? false,
  );
  // null while unknown; only warn once we positively know none is configured.
  const [hasValidation, setHasValidation] = useState<boolean | null>(null);

  useEffect(() => {
    let active = true;
    getProjectValidation(project.id)
      .then((v) => active && setHasValidation(v.commands.length > 0))
      .catch(() => active && setHasValidation(null));
    return () => {
      active = false;
    };
  }, [project.id]);

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
        independent_acceptance_tests: independentTests,
      });
      onCreated(created.id);
    } catch (e) {
      if (e instanceof ApiError && e.code === "project_toolchain_required") {
        setError(
          "This project needs a stack before you can create work orders. Open Stack to choose one.",
        );
      } else {
        setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
      }
    } finally {
      setBusy(false);
    }
  };

  const summary = [
    `${agentName(agents, agentFor(providers, "lead"))} lead · ${agentName(agents, agentFor(providers, "reviewer"))} reviewer`,
    autonomy === "run_to_completion" ? "runs to merge gate" : "stops each phase",
    merge === "auto_after_gates" ? "auto-merge" : "approval to merge",
    ...(independentTests ? ["reviewer tests"] : []),
  ].join(" · ");

  return (
    <section className="panel">
      <h2>{WORK.newAction}</h2>
      {error && <div className="banner banner--error">{error}</div>}
      <RoundsNotice />
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
            <div className="neworder__body">
              <div className="neworder__group">
                <span className="neworder__heading">Agents</span>
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
                </div>
              </div>
              <div className="neworder__group">
                <span className="neworder__heading">Workflow</span>
                <div className="neworder__grid">
                  <label>
                    Autonomy
                    <select
                      value={autonomy}
                      onChange={(e) => setAutonomy(e.target.value as AutonomyPolicy)}
                      disabled={busy}
                    >
                      <option value="review_each_phase">Stop at each phase</option>
                      <option value="run_to_completion">Run to the merge gate</option>
                    </select>
                  </label>
                  <label>
                    Merge
                    <select
                      value={merge}
                      onChange={(e) => setMerge(e.target.value as MergePolicy)}
                      disabled={busy}
                    >
                      <option value="require_user_approval">Require my approval</option>
                      <option value="auto_after_gates">Auto after gates</option>
                    </select>
                  </label>
                </div>
              </div>
              <label className="neworder__check">
                <input
                  type="checkbox"
                  checked={independentTests}
                  onChange={(e) => setIndependentTests(e.target.checked)}
                  disabled={busy}
                />
                <span>
                  Independent acceptance tests
                  <span className="neworder__hint">
                    The reviewer writes its own tests before seeing the code. Worth it when a wrong
                    understanding is costly; it roughly doubles the reviewer&apos;s work.
                  </span>
                </span>
              </label>
            </div>
          )}
        </div>

        {merge === "auto_after_gates" && hasValidation === false && (
          <NoValidationWarning
            message="No validation is set up for this project. This order will merge automatically on the two agents' approval alone — none of your own checks will run first."
            onOpenValidation={onOpenValidation}
          />
        )}

        <button className="primary" type="submit" disabled={busy || !title.trim()}>
          {busy ? "Creating…" : "Create"}
        </button>
      </form>
    </section>
  );
}

const ROUNDS_NOTICE_KEY = "commitarium.roundsNoticeDismissed";

// Shown until dismissed once: the round cap is fixed, so the user only needs
// to learn it, not configure it.
function RoundsNotice() {
  const [dismissed, setDismissed] = useState(() => {
    try {
      return localStorage.getItem(ROUNDS_NOTICE_KEY) === "1";
    } catch {
      return false;
    }
  });
  if (dismissed) return null;
  const dismiss = () => {
    try {
      localStorage.setItem(ROUNDS_NOTICE_KEY, "1");
    } catch {
      // Without storage the notice simply shows again next time.
    }
    setDismissed(true);
  };
  return (
    <div className="banner banner--info rounds-notice">
      <span>
        The agents get up to 10 rounds to agree on a plan and 10 rounds of review. If they
        haven&apos;t settled by then, the work order pauses and asks you how to continue.
      </span>
      <button type="button" className="ghost" onClick={dismiss}>
        Got it
      </button>
    </div>
  );
}
