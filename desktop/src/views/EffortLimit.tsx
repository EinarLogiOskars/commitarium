import { useEffect, useState } from "react";
import { getAssistantSettings, saveAssistantSettings } from "../api/assistant";
import { ApiError } from "../api/client";

// The highest effort Mayka may give the agents of the work she starts. It is
// kept with her settings, read afresh on saving so neither overwrites the
// other, and applies from her next turn.
export function EffortLimit() {
  const [effort, setEffort] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    getAssistantSettings()
      .then((settings) => setEffort(settings.max_effort ?? "high"))
      .catch((e) => setError(describe(e)));
  }, []);

  const choose = async (next: string) => {
    setEffort(next);
    setError(null);
    try {
      await saveAssistantSettings({ ...(await getAssistantSettings()), max_effort: next });
    } catch (e) {
      setError(describe(e));
    }
  };

  return (
    <section className="effort-limit">
      <h3>Effort limit for work orders</h3>
      <p className="muted note">
        <strong>Effort</strong> is how long a model thinks before it acts. More effort can catch
        more on hard work, like planning or tricky logic, but each turn takes longer and uses more
        of your agents&apos; limits. Mayka picks an effort for each phase of the work she starts,
        usually high for planning and medium for implementing, and never above this limit. It
        doesn&apos;t change Mayka&apos;s own replies or limit work orders you set up yourself.
      </p>
      {error && <div className="banner banner--error">{error}</div>}
      {effort && (
        <label className="field">
          <span>Highest effort</span>
          <select value={effort} onChange={(e) => void choose(e.target.value)}>
            {["low", "medium", "high", "xhigh", "max"].map((option) => (
              <option key={option} value={option}>
                {option}
              </option>
            ))}
          </select>
        </label>
      )}
    </section>
  );
}

function describe(e: unknown): string {
  return e instanceof ApiError ? e.message : String(e);
}
