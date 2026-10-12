import { useModelName } from "./useModelName";
import type { Run } from "../api/types";

// Which model is working right now, so nobody sends a small change to an
// expensive model by accident. Shown only while an agent is working.
export function AgentsAtWork({ run }: { run: Run | null }) {
  const modelName = useModelName();
  if (!run || run.status !== "running") return null;
  const working = run.sessions.filter((s) => s.status === "running");
  if (working.length === 0) return null;
  return (
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
    </p>
  );
}
