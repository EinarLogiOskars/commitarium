import type {
  AgentModels,
  AgentProviders,
  AgentRole,
  AgentProvider,
  ModelInfo,
  PhaseChoices,
} from "../api/types";

// Each role's effort, and optionally a different model and effort for
// planning (ADR-031). Controlled. The agent never changes between phases, so
// a planning model is one the role's agent offers.
export function PhaseChoiceFields({
  providers,
  models,
  choices,
  modelsFor,
  onChange,
  roles,
  planned,
  disabled,
}: {
  providers: AgentProviders;
  models: AgentModels;
  choices: PhaseChoices;
  modelsFor: (provider: AgentProvider, role: AgentRole) => ModelInfo[];
  onChange: (choices: PhaseChoices) => void;
  roles: AgentRole[];
  /** Whether the work order has a documented plan to plan with. */
  planned: boolean;
  disabled?: boolean;
}) {
  return (
    <>
      {roles.map((role) => {
        const list = modelsFor(providers[role], role);
        const planModelKey = `${role}_plan_model` as const;
        const planEffortKey = `${role}_plan_effort` as const;
        const effortKey = `${role}_effort` as const;
        const planModel = choices[planModelKey] ?? "";
        const effortsOf = (id: string) =>
          list.find((m) => m.id === id)?.supported_reasoning_efforts ?? [];
        const set = (patch: PhaseChoices) => onChange({ ...choices, ...patch });
        const label = role === "lead" ? "Lead" : "Reviewer";
        const afterPlan = role === "lead" ? "implementing" : "code review";
        const planning = role === "lead" ? "planning" : "plan review";
        return (
          <div className="agentrole" key={role}>
            <EffortSelect
              label={`${label} effort${planned ? `, ${afterPlan}` : ""}`}
              efforts={effortsOf(models[role])}
              value={choices[effortKey] ?? ""}
              onChange={(effort) => set({ [effortKey]: effort })}
              disabled={disabled}
            />
            {planned && (
              <details className="phase-choice" open={planModel !== "" || undefined}>
                <summary>Different model for {planning}</summary>
                <label>
                  {label} model for {planning}
                  <select
                    value={planModel}
                    onChange={(e) =>
                      set({
                        [planModelKey]: e.target.value,
                        // An effort the new model does not take is dropped.
                        [planEffortKey]: effortsOf(e.target.value || models[role]).includes(
                          choices[planEffortKey] ?? "",
                        )
                          ? choices[planEffortKey]
                          : "",
                      })
                    }
                    disabled={disabled || list.length === 0}
                  >
                    <option value="">Same as the {label.toLowerCase()} model</option>
                    {list.map((m) => (
                      <option key={m.id} value={m.id}>
                        {m.display_name}
                      </option>
                    ))}
                  </select>
                </label>
                <EffortSelect
                  label={`${label} effort for ${planning}`}
                  efforts={effortsOf(planModel || models[role])}
                  value={choices[planEffortKey] ?? ""}
                  onChange={(effort) => set({ [planEffortKey]: effort })}
                  disabled={disabled}
                  sameAs={`Same as ${afterPlan}`}
                />
              </details>
            )}
          </div>
        );
      })}
    </>
  );
}

function EffortSelect({
  label,
  efforts,
  value,
  onChange,
  disabled,
  sameAs = "Model default",
}: {
  label: string;
  efforts: string[];
  value: string;
  onChange: (effort: string) => void;
  disabled?: boolean;
  sameAs?: string;
}) {
  return (
    <label>
      {label}
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={disabled || efforts.length === 0}
      >
        <option value="">{sameAs}</option>
        {efforts.map((effort) => (
          <option key={effort} value={effort}>
            {effort}
          </option>
        ))}
      </select>
    </label>
  );
}

/** The choices that still apply: a role that does not run, or planning
 * without a plan, keeps nothing. */
export function applicablePhaseChoices(
  choices: PhaseChoices,
  roles: AgentRole[],
  planned: boolean,
): PhaseChoices {
  const kept: PhaseChoices = {};
  for (const role of roles) {
    const effort = choices[`${role}_effort`];
    if (effort) kept[`${role}_effort`] = effort;
    if (!planned) continue;
    const planModel = choices[`${role}_plan_model`];
    const planEffort = choices[`${role}_plan_effort`];
    if (planModel) kept[`${role}_plan_model`] = planModel;
    if (planEffort) kept[`${role}_plan_effort`] = planEffort;
  }
  return kept;
}

/** Drops efforts the roles' models no longer accept, as when a role's agent
 * or model changes. */
export function keepSupportedEfforts(
  choices: PhaseChoices,
  providers: AgentProviders,
  models: AgentModels,
  modelsFor: (provider: AgentProvider, role: AgentRole) => ModelInfo[],
): PhaseChoices {
  const kept: PhaseChoices = { ...choices };
  for (const role of ["lead", "reviewer"] as const) {
    const list = modelsFor(providers[role], role);
    const accepts = (id: string, effort: string | undefined) =>
      !effort ||
      (list.find((m) => m.id === id)?.supported_reasoning_efforts ?? []).includes(effort);
    if (choices[`${role}_plan_model`] && !list.some((m) => m.id === choices[`${role}_plan_model`]))
      kept[`${role}_plan_model`] = "";
    if (!accepts(models[role], choices[`${role}_effort`])) kept[`${role}_effort`] = "";
    if (!accepts(kept[`${role}_plan_model`] || models[role], choices[`${role}_plan_effort`]))
      kept[`${role}_plan_effort`] = "";
  }
  return kept;
}
