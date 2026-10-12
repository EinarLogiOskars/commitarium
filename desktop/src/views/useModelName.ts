import { useEffect, useState } from "react";
import { getModels } from "../api/projects";
import type { AgentProvider, AgentRole, ModelCatalog, ModelInfo } from "../api/types";

// The stored model catalogs, fetched once and shared, to show a model's
// display name wherever an agent is named.
let catalogs: Promise<ModelCatalog[]> | null = null;

function loadCatalogs(): Promise<ModelCatalog[]> {
  catalogs ??= getModels()
    .then((r) => r.catalogs)
    .catch(() => {
      catalogs = null;
      return [];
    });
  return catalogs;
}

/** Returns a function naming a model by its display name, or its ID until
 * the catalogs load (or when no catalog lists it). */
export function useModelName(): (modelId: string | undefined) => string {
  const [names, setNames] = useState<Map<string, string>>(new Map());
  useEffect(() => {
    let active = true;
    void loadCatalogs().then((loaded) => {
      if (!active) return;
      const map = new Map<string, string>();
      for (const catalog of loaded)
        for (const model of catalog.models) map.set(model.id, model.display_name);
      setNames(map);
    });
    return () => {
      active = false;
    };
  }, []);
  return (modelId) => (modelId ? (names.get(modelId) ?? modelId) : "");
}

/** The stored model catalogs (shared and cached), as a lookup of the models
 * an agent's provider offers for a role. */
export function useCatalogModels(): (provider: AgentProvider, role: AgentRole) => ModelInfo[] {
  const [loaded, setLoaded] = useState<ModelCatalog[]>([]);
  useEffect(() => {
    let active = true;
    void loadCatalogs().then((catalogs) => active && setLoaded(catalogs));
    return () => {
      active = false;
    };
  }, []);
  return (provider, role) =>
    loaded.find((catalog) => catalog.provider === provider && catalog.role === role)?.models ?? [];
}
