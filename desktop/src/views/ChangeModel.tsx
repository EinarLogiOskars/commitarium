import { useState } from "react";
import { changeRunModels } from "../api/runs";
import { ApiError } from "../api/client";
import { useModels } from "./useModels";
import type { AgentRole, Run } from "../api/types";

// Switching a working run's model, so a wrong pick doesn't need a reset: stop
// the agent now and resume it on the new model, or let its turn finish and
// switch from the next one. Each role keeps its agent.
export function ChangeModel({
  run,
  roles,
  onChanged,
  onClose,
}: {
  run: Run;
  roles: AgentRole[];
  onChanged: () => void;
  onClose: () => void;
}) {
  const { modelsFor, loading } = useModels();
  const [models, setModels] = useState<Record<AgentRole, string>>({
    lead: run.agent_models?.lead ?? "",
    reviewer: run.agent_models?.reviewer ?? "",
  });
  const [busy, setBusy] = useState<null | "now" | "next_turn">(null);
  const [error, setError] = useState<string | null>(null);
  const working = run.status === "running" && run.sessions.some((s) => s.status === "running");
  const changed = roles.some((role) => models[role] && models[role] !== run.agent_models?.[role]);

  const apply = async (when: "now" | "next_turn") => {
    setBusy(when);
    setError(null);
    try {
      await changeRunModels(run.id, {
        ...(models.lead !== run.agent_models?.lead ? { lead_model: models.lead } : {}),
        ...(models.reviewer !== run.agent_models?.reviewer
          ? { reviewer_model: models.reviewer }
          : {}),
        when,
      });
      onChanged();
      onClose();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    } finally {
      setBusy(null);
    }
  };

  return (
    <div className="change-model">
      {roles.map((role) => {
        const provider = run.agent_providers?.[role];
        const options = provider ? modelsFor(provider, role) : [];
        const current = run.agent_models?.[role] ?? "";
        return (
          <label key={role} className="change-model__role">
            <span>{role === "lead" ? "Lead" : "Reviewer"}</span>
            <select
              value={models[role]}
              onChange={(e) => setModels((m) => ({ ...m, [role]: e.target.value }))}
              disabled={busy !== null || loading}
            >
              {!options.some((m) => m.id === current) && current && (
                <option value={current}>{current}</option>
              )}
              {options.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.display_name}
                </option>
              ))}
            </select>
          </label>
        );
      })}
      {error && <div className="banner banner--error">{error}</div>}
      <div className="row">
        {working ? (
          <>
            <button
              className="primary"
              onClick={() => void apply("now")}
              disabled={!changed || busy !== null}
              title="Stops the agent mid-turn and resumes its conversation on the new model. Its file changes so far are kept."
            >
              {busy === "now" ? "Switching…" : "Stop now and switch"}
            </button>
            <button
              onClick={() => void apply("next_turn")}
              disabled={!changed || busy !== null}
              title="Lets the current turn finish on the old model."
            >
              {busy === "next_turn" ? "Switching…" : "Switch at the next turn"}
            </button>
          </>
        ) : (
          <button
            className="primary"
            onClick={() => void apply("next_turn")}
            disabled={!changed || busy !== null}
          >
            {busy ? "Switching…" : "Switch model"}
          </button>
        )}
        <button className="ghost" onClick={onClose} disabled={busy !== null}>
          Cancel
        </button>
      </div>
    </div>
  );
}
