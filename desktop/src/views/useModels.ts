import { useCallback, useEffect, useState } from "react";
import { getModels, refreshModels } from "../api/projects";
import { ApiError } from "../api/client";
import type { AgentProvider, AgentRole, ModelCatalog, ModelInfo } from "../api/types";

/** Load the provider+role model catalogs and expose a per-(provider,role) lookup. */
export function useModels() {
  const [catalogs, setCatalogs] = useState<ModelCatalog[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async (refresh = false) => {
    setLoading(true);
    setError(null);
    try {
      const r = refresh ? await refreshModels() : await getModels();
      setCatalogs(r.catalogs);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const modelsFor = useCallback(
    (provider: AgentProvider, role: AgentRole): ModelInfo[] =>
      catalogs.find((c) => c.provider === provider && c.role === role)?.models ?? [],
    [catalogs],
  );

  return { catalogs, modelsFor, loading, error, refresh: () => load(true) };
}

/** Keep `current` if it's still a valid option, else fall back to the first. */
export function pickModel(models: ModelInfo[], current: string): string {
  if (models.some((m) => m.id === current)) return current;
  return models[0]?.id ?? "";
}
