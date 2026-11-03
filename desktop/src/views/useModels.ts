import { useCallback, useEffect, useState } from "react";
import { getModels, refreshModels } from "../api/projects";
import { ApiError } from "../api/client";
import type { AgentProvider, AgentRole, ModelCatalog, ModelInfo } from "../api/types";

/** Load the provider+role model catalogs, refreshed from the workers on open,
 * and expose a per-(provider,role) lookup. */
/** How often, and how long, to ask again for a provider whose models have
 * not come yet: a worker just added is still starting. */
const WAIT_EVERY_MS = 4000;
const WAIT_TRIES = 45;

/** waitFor names a provider whose models the view needs: until they come,
 * it asks the workers again every few seconds. */
export function useModels(waitFor?: AgentProvider) {
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

  // Show the stored catalogs at once, then ask the agents' workers for their
  // current models so a form always offers what the providers offer now.
  useEffect(() => {
    let active = true;
    void load().then(() =>
      refreshModels()
        .then((r) => active && setCatalogs(r.catalogs))
        .catch(() => undefined),
    );
    return () => {
      active = false;
    };
  }, [load]);

  const modelsFor = useCallback(
    (provider: AgentProvider, role: AgentRole): ModelInfo[] =>
      catalogs.find((c) => c.provider === provider && c.role === role)?.models ?? [],
    [catalogs],
  );

  const waiting = !!waitFor && modelsFor(waitFor, "lead").length === 0;
  useEffect(() => {
    if (!waiting) return;
    let tries = 0;
    const timer = setInterval(() => {
      tries += 1;
      if (tries > WAIT_TRIES) clearInterval(timer);
      void refreshModels()
        .then((r) => setCatalogs(r.catalogs))
        .catch(() => undefined);
    }, WAIT_EVERY_MS);
    return () => clearInterval(timer);
  }, [waiting]);

  return { catalogs, modelsFor, loading, error, waiting, refresh: () => load(true) };
}

/** Keep `current` if it's still a valid option, else fall back to the first. */
export function pickModel(models: ModelInfo[], current: string): string {
  if (models.some((m) => m.id === current)) return current;
  return models[0]?.id ?? "";
}
