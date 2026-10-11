import { useCallback, useEffect, useState } from "react";
import { checkAgentCliUpdates, type CliStatus } from "../ipc";

/** Checks once, when the stack is usable, whether the agents' provider CLIs
 * have newer releases; newer CLIs also bring newer models. */
export function useAgentCliUpdates(enabled: boolean) {
  const [statuses, setStatuses] = useState<CliStatus[]>([]);

  const refresh = useCallback(async () => {
    try {
      setStatuses(await checkAgentCliUpdates());
    } catch {
      /* offline or no workers: nothing to offer */
    }
  }, []);

  useEffect(() => {
    if (enabled) void refresh();
  }, [enabled, refresh]);

  return { statuses, available: statuses.filter((s) => s.updateAvailable), refresh };
}

export type AgentCliUpdates = ReturnType<typeof useAgentCliUpdates>;
