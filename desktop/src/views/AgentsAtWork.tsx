import { useState } from "react";
import { useModelName } from "./useModelName";
import { ChangeModel } from "./ChangeModel";
import type { AgentRole, Run } from "../api/types";

// Which model is working right now, so nobody sends a small change to an
// expensive model by accident, and a way to switch it without a reset.
export function AgentsAtWork({ run, onChanged }: { run: Run | null; onChanged?: () => void }) {
  const modelName = useModelName();
  const [changing, setChanging] = useState(false);
  if (
    !run ||
    !run.agent_models?.lead ||
    run.status === "succeeded" ||
    run.status === "stopped" ||
    run.status === "failed"
  ) {
    return null;
  }
  const working =
    run.status === "running" ? run.sessions.filter((s) => s.status === "running") : [];
  const roles: AgentRole[] =
    run.steps?.reviewer === false && !run.sessions.some((s) => s.role === "reviewer")
      ? ["lead"]
      : ["lead", "reviewer"];
  const change = onChanged && (
    <button className="linkish agents-at-work__change" onClick={() => setChanging((v) => !v)}>
      Change model
    </button>
  );
  return (
    <>
      {working.length > 0 ? (
        <p className="agents-at-work">
          <span className="agents-at-work__dot" aria-hidden />
          Working now:{" "}
          {working.map((s, index) => {
            const role = s.role === "reviewer" ? "reviewer" : "lead";
            const model = modelName(run.agent_models?.[role]);
            return (
              <span key={s.id}>
                {index > 0 && " · "}
                <strong>{role === "lead" ? "Lead" : "Reviewer"}</strong>
                {model && ` · ${model}`}
                {s.agent_id && <span className="muted"> ({s.agent_id})</span>}
              </span>
            );
          })}
          {change}
        </p>
      ) : (
        <p className="muted agents-at-work agents-at-work--idle">
          {roles
            .map(
              (role) =>
                `${role === "lead" ? "Lead" : "Reviewer"} · ${modelName(run.agent_models?.[role])}`,
            )
            .join("  ·  ")}
          {change}
        </p>
      )}
      {changing && onChanged && (
        <ChangeModel
          run={run}
          roles={roles}
          onChanged={onChanged}
          onClose={() => setChanging(false)}
        />
      )}
    </>
  );
}
