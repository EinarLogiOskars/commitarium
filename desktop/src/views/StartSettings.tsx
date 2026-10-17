import { useEffect, useState } from "react";
import { getProjectValidation } from "../api/projects";
import { AgentModelFields } from "./AgentModelFields";
import { NoValidationWarning } from "./NoValidationWarning";
import {
  applicablePhaseChoices,
  keepSupportedEfforts,
  PhaseChoiceFields,
} from "./PhaseChoiceFields";
import { pickModel, useModels } from "./useModels";
import { useWorkSuggestion } from "./useWorkSuggestion";
import { choiceMade } from "./workChoice";
import type {
  AgentModels,
  AgentProviders,
  AutonomyPolicy,
  Feature,
  MergePolicy,
  PhaseChoices,
  WorkSteps,
} from "../api/types";

/** The steps chosen at Start. `clarification: false` starts a Draft with its
 * title and description as the goal; it is only sent from a Draft. */
export interface StartSteps extends WorkSteps {
  clarification?: boolean;
}

/** What a work order starts with: chosen at Start, not while drafting. */
export interface StartSettingsValue {
  agent_providers: AgentProviders;
  agent_models: AgentModels;
  autonomy_policy: AutonomyPolicy;
  merge_policy: MergePolicy;
  independent_acceptance_tests: boolean;
  steps: StartSteps;
  /** Efforts and planning models by phase (ADR-031). */
  phase_choices: PhaseChoices;
}

/** The valid combinations of a work order's steps (ADR-018): a reviewer
 * requires a documented plan. */
const PRESETS: { id: string; label: string; steps: WorkSteps; hint: string }[] = [
  {
    id: "full",
    label: "Full",
    steps: { documented_plan: true, reviewer: true },
    hint: "A reviewer checks the plan and the code before the merge gate.",
  },
  {
    id: "planned",
    label: "Planned",
    steps: { documented_plan: true, reviewer: false },
    hint: "The lead plans the commits alone and implements them. You decide the merge.",
  },
  {
    id: "quick",
    label: "Quick change",
    steps: { documented_plan: false, reviewer: false },
    hint: "The lead goes straight to the change in one turn. You decide the merge.",
  },
];

/** Not chosen yet: the form fills in a suggestion (ADR-027). */
const UNCHOSEN: AgentProviders = {
  lead: "codex",
  reviewer: "codex",
  lead_agent: "",
  reviewer_agent: "",
};

/** The work order's current settings as the starting point for the form. A
 * work order whose agents are not chosen yet starts from a suggestion the
 * user can change; a project has no defaults. */
export function startSettingsFrom(feature: Feature): StartSettingsValue {
  const chosen = choiceMade(feature.agent_providers, feature.agent_models);
  return {
    agent_providers: chosen && feature.agent_providers ? feature.agent_providers : UNCHOSEN,
    agent_models: {
      lead: chosen ? (feature.agent_models?.lead ?? "") : "",
      reviewer: chosen ? (feature.agent_models?.reviewer ?? "") : "",
    },
    autonomy_policy: feature.autonomy_policy ?? "review_each_phase",
    merge_policy: feature.merge_policy ?? "require_user_approval",
    independent_acceptance_tests: feature.independent_acceptance_tests ?? false,
    steps: feature.steps ?? { documented_plan: true, reviewer: true },
    phase_choices: feature.phase_choices ?? {},
  };
}

/** What a Start sends: agents and models only once they are chosen, so a
 * Start without any is refused with a reason rather than sent half-made. */
export function startBody(value: StartSettingsValue): Partial<StartSettingsValue> {
  const roles: ("lead" | "reviewer")[] = value.steps.reviewer ? ["lead", "reviewer"] : ["lead"];
  const phased = {
    ...value,
    phase_choices: applicablePhaseChoices(value.phase_choices, roles, value.steps.documented_plan),
  };
  if (choiceMade(value.agent_providers, value.agent_models)) return phased;
  const rest: Partial<StartSettingsValue> = { ...phased };
  delete rest.agent_providers;
  delete rest.agent_models;
  return rest;
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
  // Agents and models not chosen yet start from a suggestion (ADR-027):
  // the freshest agent leads, another provider reviews.
  const suggestion = useWorkSuggestion();
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
  const { agent_providers: providers, agent_models: models, steps } = value;
  const preset = PRESETS.find(
    (p) => p.steps.documented_plan === steps.documented_plan && p.steps.reviewer === steps.reviewer,
  );
  useEffect(() => {
    if (suggestion && !choiceMade(value.agent_providers, value.agent_models))
      onChange({ ...value, ...suggestion });
  }, [suggestion, value, onChange]);
  useEffect(() => {
    if (modelsLoading || !providers.lead_agent) return;
    const lead = pickModel(modelsFor(providers.lead, "lead"), models.lead);
    const reviewer = pickModel(modelsFor(providers.reviewer, "reviewer"), models.reviewer);
    if (lead !== models.lead || reviewer !== models.reviewer)
      onChange({ ...value, agent_models: { lead, reviewer } });
  }, [modelsLoading, modelsFor, providers, models, value, onChange]);

  return (
    <div className="neworder__body">
      <div className="neworder__group">
        <span className="neworder__heading">How it runs</span>
        <div className="steps-choice" role="radiogroup" aria-label="How it runs">
          {PRESETS.map((p) => {
            const on = preset?.id === p.id;
            return (
              <button
                key={p.id}
                type="button"
                role="radio"
                aria-checked={on}
                className={`steps-choice__opt ${on ? "steps-choice__opt--on" : ""}`}
                onClick={() =>
                  onChange({
                    ...value,
                    steps: { ...steps, ...p.steps },
                    // Acceptance tests are the reviewer's.
                    independent_acceptance_tests: p.steps.reviewer
                      ? value.independent_acceptance_tests
                      : false,
                  })
                }
                disabled={disabled}
              >
                <span className="steps-choice__title">{p.label}</span>
                <span className="steps-choice__parts">
                  <StepPart on={p.steps.documented_plan}>Plan</StepPart>
                  <StepPart on={p.steps.reviewer}>Reviewer</StepPart>
                </span>
                <span className="steps-choice__hint">{p.hint}</span>
              </button>
            );
          })}
        </div>
      </div>
      <div className="neworder__group">
        <span className="neworder__heading">Agents</span>
        {suggestion === null && !providers.lead_agent && (
          <p className="muted note">
            No agent can take this work right now. Add or connect one on the Agents page, or wait
            for a usage limit to reset.
          </p>
        )}
        <div className="neworder__grid">
          <AgentModelFields
            providers={providers}
            models={models}
            modelsFor={modelsFor}
            onChange={(p, m) =>
              onChange({
                ...value,
                agent_providers: p,
                agent_models: m,
                phase_choices: keepSupportedEfforts(value.phase_choices, p, m, modelsFor),
              })
            }
            disabled={disabled}
            roles={steps.reviewer ? ["lead", "reviewer"] : ["lead"]}
          />
          <PhaseChoiceFields
            providers={providers}
            models={models}
            choices={value.phase_choices}
            modelsFor={modelsFor}
            onChange={(phase_choices) => onChange({ ...value, phase_choices })}
            roles={steps.reviewer ? ["lead", "reviewer"] : ["lead"]}
            planned={steps.documented_plan}
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
      {steps.reviewer && (
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
      )}
      {value.merge_policy === "auto_after_gates" && hasValidation === false && (
        <NoValidationWarning
          message={
            steps.reviewer
              ? "No validation is set up for this project. This order will merge automatically on the two agents' approval alone — none of your own checks will run first."
              : "No validation is set up for this project, and this order has no reviewer. It will merge automatically on the lead's word alone — nothing will check it first."
          }
          onOpenValidation={onOpenValidation ?? (() => undefined)}
        />
      )}
    </div>
  );
}

function StepPart({ on, children }: { on: boolean; children: string }) {
  return (
    <span className={`steps-choice__part ${on ? "" : "steps-choice__part--off"}`}>
      {on ? "✓" : "–"} {children}
    </span>
  );
}
