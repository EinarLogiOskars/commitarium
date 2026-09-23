import { useEffect } from "react";
import { AgentModelFields } from "./AgentModelFields";
import { pickModel } from "./useModels";
import type {
  AgentModels,
  AgentProvider,
  AgentProviders,
  AgentRole,
  AutonomyPolicy,
  CreateProjectInput,
  MergePolicy,
  ModelInfo,
  Project,
  RecoveryPolicy,
} from "../api/types";

/** The project-level defaults a new project can carry. New work orders inherit
 * these and may override them per order. */
export interface ProjectDefaults {
  recovery_policy: RecoveryPolicy;
  providers: AgentProviders;
  models: AgentModels;
  autonomy_policy: AutonomyPolicy;
  merge_policy: MergePolicy;
  planning_rounds: number;
  review_rounds: number;
}

/** Safe starting defaults for a brand-new project. */
export function initialProjectDefaults(): ProjectDefaults {
  return {
    recovery_policy: "approval_required",
    providers: { lead: "codex", reviewer: "codex" },
    models: { lead: "", reviewer: "" },
    autonomy_policy: "review_each_phase",
    merge_policy: "require_user_approval",
    planning_rounds: 6,
    review_rounds: 6,
  };
}

/** Seed defaults from an existing project (used when defaults are edited later). */
export function projectDefaultsFrom(p: Project): ProjectDefaults {
  const d = initialProjectDefaults();
  return {
    recovery_policy: p.recovery_policy ?? d.recovery_policy,
    providers: p.agent_providers ?? d.providers,
    models: { lead: p.agent_models?.lead ?? "", reviewer: p.agent_models?.reviewer ?? "" },
    autonomy_policy: p.autonomy_policy ?? d.autonomy_policy,
    merge_policy: p.merge_policy ?? d.merge_policy,
    planning_rounds: p.dialogue_limits?.planning_rounds ?? d.planning_rounds,
    review_rounds: p.dialogue_limits?.implementation_review_rounds ?? d.review_rounds,
  };
}

/** Turn defaults into the create/import payload extras. agent_models is omitted
 * when unresolved so the backend applies its own model defaults. */
export function projectDefaultsPayload(d: ProjectDefaults): Omit<CreateProjectInput, "name"> {
  return {
    recovery_policy: d.recovery_policy,
    agent_providers: d.providers,
    ...(d.models.lead && d.models.reviewer ? { agent_models: d.models } : {}),
    autonomy_policy: d.autonomy_policy,
    merge_policy: d.merge_policy,
    dialogue_limits: {
      planning_rounds: d.planning_rounds,
      implementation_review_rounds: d.review_rounds,
    },
  };
}

export function ProjectDefaultsFields({
  value,
  onChange,
  modelsFor,
  modelsLoading,
  disabled,
}: {
  value: ProjectDefaults;
  onChange: (d: ProjectDefaults) => void;
  modelsFor: (provider: AgentProvider, role: AgentRole) => ModelInfo[];
  modelsLoading: boolean;
  disabled?: boolean;
}) {
  // Resolve each role's model to a valid choice once catalogs load or a provider
  // changes; keep the current pick when it's still offered.
  useEffect(() => {
    if (modelsLoading) return;
    const lead = pickModel(modelsFor(value.providers.lead, "lead"), value.models.lead);
    const reviewer = pickModel(
      modelsFor(value.providers.reviewer, "reviewer"),
      value.models.reviewer,
    );
    if (lead !== value.models.lead || reviewer !== value.models.reviewer) {
      onChange({ ...value, models: { lead, reviewer } });
    }
  }, [modelsLoading, modelsFor, value, onChange]);

  return (
    <div className="neworder__grid">
      <AgentModelFields
        providers={value.providers}
        models={value.models}
        modelsFor={modelsFor}
        onChange={(providers, models) => onChange({ ...value, providers, models })}
        disabled={disabled}
      />
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
      <label>
        Recovery
        <select
          value={value.recovery_policy}
          onChange={(e) =>
            onChange({ ...value, recovery_policy: e.target.value as RecoveryPolicy })
          }
          disabled={disabled}
        >
          <option value="approval_required">Approval required</option>
          <option value="automatic">Automatic recovery</option>
        </select>
      </label>
      <label>
        Planning rounds
        <input
          type="number"
          min={0}
          value={value.planning_rounds}
          onChange={(e) =>
            onChange({ ...value, planning_rounds: Math.max(0, Number(e.target.value)) })
          }
          disabled={disabled}
        />
      </label>
      <label>
        Review rounds
        <input
          type="number"
          min={0}
          value={value.review_rounds}
          onChange={(e) =>
            onChange({ ...value, review_rounds: Math.max(0, Number(e.target.value)) })
          }
          disabled={disabled}
        />
      </label>
    </div>
  );
}
