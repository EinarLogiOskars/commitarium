import { request } from "./client";

// The official model guide merged with the user's overrides (ADR-031).

export type ModelTier = "premium" | "frontier" | "strong" | "light" | "avoid";

export interface ModelGuideEntry {
  id: string;
  /** Absent for an unrated model. */
  tier?: ModelTier;
  note?: string;
  source: "official" | "user" | "unrated";
  official_tier?: ModelTier;
  /** The official tier changed since the user set theirs. */
  official_changed?: boolean;
}

export interface ModelGuide {
  as_of: string;
  models: ModelGuideEntry[];
}

export const TIERS: { value: ModelTier; label: string; hint: string }[] = [
  { value: "premium", label: "Premium", hint: "Mayka asks you first" },
  { value: "frontier", label: "Frontier", hint: "Planning, plan review, hard problems" },
  { value: "strong", label: "Strong", hint: "Implementation and code review" },
  { value: "light", label: "Light", hint: "Mechanical changes" },
  { value: "avoid", label: "Avoid", hint: "Not chosen while others fit" },
];

export const tierLabel = (tier: ModelTier | undefined): string =>
  TIERS.find((t) => t.value === tier)?.label ?? "Not rated";

/** Whether the assistant must ask before using the model. */
export const needsApproval = (entry: ModelGuideEntry | undefined): boolean =>
  !entry || entry.source === "unrated" || entry.tier === "premium";

const overridePath = (model: string) =>
  `/api/v1/model-guide/overrides/${encodeURIComponent(model)}`;

export const getModelGuide = (): Promise<ModelGuide> => request("/api/v1/model-guide");

export const setModelTier = (model: string, tier: ModelTier): Promise<ModelGuideEntry> =>
  request(overridePath(model), { method: "PUT", body: { tier } });

export const resetModelTier = (model: string): Promise<ModelGuideEntry> =>
  request(overridePath(model), { method: "DELETE" });
