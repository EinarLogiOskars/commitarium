import type {
  AutonomyPolicy,
  CreateProjectInput,
  MergePolicy,
  Project,
  RecoveryPolicy,
} from "../api/types";

/** The project-level defaults a new project can carry. New work orders inherit
 * these and may override them per order. Agents and models are not among
 * them (ADR-027): each work order's are chosen when it starts. */
export interface ProjectDefaults {
  recovery_policy: RecoveryPolicy;
  autonomy_policy: AutonomyPolicy;
  merge_policy: MergePolicy;
}

/** Safe starting defaults for a brand-new project. */
export function initialProjectDefaults(): ProjectDefaults {
  return {
    recovery_policy: "approval_required",
    autonomy_policy: "review_each_phase",
    merge_policy: "require_user_approval",
  };
}

/** Seed defaults from an existing project (used when defaults are edited later). */
export function projectDefaultsFrom(p: Project): ProjectDefaults {
  const d = initialProjectDefaults();
  return {
    recovery_policy: p.recovery_policy ?? d.recovery_policy,
    autonomy_policy: p.autonomy_policy ?? d.autonomy_policy,
    merge_policy: p.merge_policy ?? d.merge_policy,
  };
}

/** Turn defaults into the create/import payload extras. */
export function projectDefaultsPayload(d: ProjectDefaults): Omit<CreateProjectInput, "name"> {
  return {
    recovery_policy: d.recovery_policy,
    autonomy_policy: d.autonomy_policy,
    merge_policy: d.merge_policy,
  };
}

export function ProjectDefaultsFields({
  value,
  onChange,
  disabled,
}: {
  value: ProjectDefaults;
  onChange: (d: ProjectDefaults) => void;
  disabled?: boolean;
}) {
  return (
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
    </div>
  );
}
