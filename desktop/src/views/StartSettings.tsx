import { useEffect, useState } from "react";
import { getProjectValidation } from "../api/projects";
import { AgentModelFields } from "./AgentModelFields";
import { NoValidationWarning } from "./NoValidationWarning";
import { pickModel, useModels } from "./useModels";
import type {
  AgentModels,
  AgentProviders,
  AutonomyPolicy,
  Feature,
  MergePolicy,
} from "../api/types";

/** What a work order starts with: chosen at Start, not while drafting. */
export interface StartSettingsValue {
  agent_providers: AgentProviders;
  agent_models: AgentModels;
  autonomy_policy: AutonomyPolicy;
  merge_policy: MergePolicy;
  independent_acceptance_tests: boolean;
}

/** The work order's current settings — its project's defaults until a Start
 * chose others — as the starting point for the form. */
export function startSettingsFrom(feature: Feature): StartSettingsValue {
  return {
    agent_providers: feature.agent_providers ?? { lead: "codex", reviewer: "codex" },
    agent_models: {
      lead: feature.agent_models?.lead ?? "",
      reviewer: feature.agent_models?.reviewer ?? "",
    },
    autonomy_policy: feature.autonomy_policy ?? "review_each_phase",
    merge_policy: feature.merge_policy ?? "require_user_approval",
    independent_acceptance_tests: feature.independent_acceptance_tests ?? false,
  };
}

/** Agents, models, and workflow settings for a Start. Controlled. */
export function StartSettings({
  projectId,
  value,
  onChange,
  onOpenValidation,
  disabled,
}: {
  projectId: string;
  value: StartSettingsValue;
  onChange: (value: StartSettingsValue) => void;
  onOpenValidation?: () => void;
  disabled?: boolean;
}) {
  const { modelsFor, loading: modelsLoading } = useModels();
  // null while unknown; only warn once we positively know none is configured.
  const [hasValidation, setHasValidation] = useState<boolean | null>(null);

  useEffect(() => {
    let active = true;
    getProjectValidation(projectId)
      .then((v) => active && setHasValidation(v.commands.length > 0))
      .catch(() => active && setHasValidation(null));
    return () => {
      active = false;
    };
  }, [projectId]);

  // Resolve each role's model to one the agent's provider offers.
  const { agent_providers: providers, agent_models: models } = value;
  useEffect(() => {
    if (modelsLoading) return;
    const lead = pickModel(modelsFor(providers.lead, "lead"), models.lead);
    const reviewer = pickModel(modelsFor(providers.reviewer, "reviewer"), models.reviewer);
    if (lead !== models.lead || reviewer !== models.reviewer)
      onChange({ ...value, agent_models: { lead, reviewer } });
  }, [modelsLoading, modelsFor, providers, models, value, onChange]);

  return (
    <div className="neworder__body">
      <div className="neworder__group">
        <span className="neworder__heading">Agents</span>
        <div className="neworder__grid">
          <AgentModelFields
            providers={providers}
            models={models}
            modelsFor={modelsFor}
            onChange={(p, m) => onChange({ ...value, agent_providers: p, agent_models: m })}
            disabled={disabled}
          />
        </div>
      </div>
      <div className="neworder__group">
        <span className="neworder__heading">Workflow</span>
        <div className="neworder__grid">
          <label>
            Autonomy
            <select
              value={value.autonomy_policy}
              onChange={(e) =>
                onChange({ ...value, autonomy_policy: e.target.value as AutonomyPolicy })
              }
              disabled={disabled}
            >
              <option value="review_each_phase">Stop at each phase</option>
              <option value="run_to_completion">Run to the merge gate</option>
            </select>
          </label>
          <label>
            Merge
            <select
              value={value.merge_policy}
              onChange={(e) => onChange({ ...value, merge_policy: e.target.value as MergePolicy })}
              disabled={disabled}
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
          checked={value.independent_acceptance_tests}
          onChange={(e) => onChange({ ...value, independent_acceptance_tests: e.target.checked })}
          disabled={disabled}
        />
        <span>
          Independent acceptance tests
          <span className="neworder__hint">
            The reviewer writes its own tests before seeing the code. Worth it when a wrong
            understanding is costly; it roughly doubles the reviewer&apos;s work.
          </span>
        </span>
      </label>
      {value.merge_policy === "auto_after_gates" && hasValidation === false && (
        <NoValidationWarning
          message="No validation is set up for this project. This order will merge automatically on the two agents' approval alone — none of your own checks will run first."
          onOpenValidation={onOpenValidation ?? (() => undefined)}
        />
      )}
    </div>
  );
}
