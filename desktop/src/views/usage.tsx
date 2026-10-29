import { Fragment, useEffect, useState } from "react";
import { getFeatureUsage } from "../api/features";
import { getProjectUsage } from "../api/projects";
import type { CostSummary, FeatureUsage, PhaseUsage, ProjectUsage, TokenUsage } from "../api/types";
import { useModelName } from "./useModelName";

// Token usage and what it cost (ADR-031): API-key turns in dollars, priced by
// the provider or the model guide; subscription turns in tokens.

// Input is everything the provider read: fresh, cached, and cache writes.
// Cached reads are listed separately because they are much cheaper.
export const readTokens = (u: TokenUsage) =>
  u.input_tokens + u.cached_input_tokens + u.cache_write_tokens;

export const PHASE_LABELS: Record<string, string> = {
  clarify: "Clarify",
  plan: "Plan",
  plan_approval: "Plan approval",
  implement: "Implement",
  acceptance_tests: "Acceptance tests",
  review: "Review",
  correction: "Correction",
  readiness: "Readiness",
  intervention: "Your messages",
  other: "Other",
};

export function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`;
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`;
  return String(n);
}

export function formatUSD(n: number): string {
  if (n > 0 && n < 0.01) return "< $0.01";
  return `$${n.toFixed(2)}`;
}

const tokensLine = (u: TokenUsage) =>
  `${formatTokens(readTokens(u))} in (${formatTokens(u.cached_input_tokens)} cached) · ${formatTokens(u.output_tokens)} out`;

const anyTokens = (u: TokenUsage) => readTokens(u) + u.output_tokens > 0;

/** The parts of a cost summary worth showing, in the user's words. */
export function costParts(cost: CostSummary): { label: string; value: string; note?: string }[] {
  const parts: { label: string; value: string; note?: string }[] = [];
  if (cost.api_cost_usd > 0)
    parts.push({
      label: "API keys",
      value: `≈ ${formatUSD(cost.api_cost_usd)}`,
      note: cost.prices_as_of ? `prices as of ${cost.prices_as_of}` : undefined,
    });
  if (anyTokens(cost.api_unpriced))
    parts.push({
      label: "API keys, no price known",
      value: tokensLine(cost.api_unpriced),
    });
  if (anyTokens(cost.subscription))
    parts.push({ label: "Subscriptions", value: tokensLine(cost.subscription) });
  if (anyTokens(cost.unknown))
    parts.push({
      label: "Billing not reported",
      value: tokensLine(cost.unknown),
      note: "turns from before billing was recorded",
    });
  return parts;
}

function CostParts({ cost }: { cost: CostSummary }) {
  const parts = costParts(cost);
  if (parts.length === 0) return <p className="muted note">No usage recorded yet.</p>;
  return (
    <dl className="detail cost__parts">
      {parts.map((part) => (
        <Fragment key={part.label}>
          <dt>{part.label}</dt>
          <dd>
            {part.value}
            {part.note && <span className="muted"> · {part.note}</span>}
          </dd>
        </Fragment>
      ))}
    </dl>
  );
}

export function UsageTable({ phases, roles }: { phases: PhaseUsage[]; roles: string[] }) {
  const modelName = useModelName();
  // Phases arrive in workflow order; keep the first occurrence of each.
  const order = phases.map((p) => p.phase).filter((p, i, all) => all.indexOf(p) === i);
  const cell = (phase: string, role: string) => {
    const u = phases.find((p) => p.phase === phase && p.role === role);
    if (!u) return <td className="muted">—</td>;
    return (
      <td>
        {formatTokens(readTokens(u))} in
        <span className="muted"> ({formatTokens(u.cached_input_tokens)} cached)</span> ·{" "}
        {formatTokens(u.output_tokens)} out
        {(u.models ?? []).map((m) => (
          <span key={`${m.model}:${m.effort ?? ""}`} className="usage-table__model muted">
            {modelName(m.model)}
            {m.effort ? ` · ${m.effort}` : ""}
            {(u.models ?? []).length > 1 ? ` · ${formatTokens(readTokens(m))} in` : ""}
            {m.cost_usd !== undefined ? ` · ≈ ${formatUSD(m.cost_usd)}` : ""}
          </span>
        ))}
      </td>
    );
  };
  return (
    <table className="usage-table">
      <thead>
        <tr>
          <th>Phase</th>
          {roles.map((r) => (
            <th key={r}>{r}</th>
          ))}
        </tr>
      </thead>
      <tbody>
        {order.map((phase) => (
          <tr key={phase}>
            <td>{PHASE_LABELS[phase] ?? phase}</td>
            {roles.map((r) => (
              <Fragment key={r}>{cell(phase, r)}</Fragment>
            ))}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

/** A finished work order's cost, as its own card under Merge. */
export function WorkOrderCost({ projectId, featureId }: { projectId: string; featureId: string }) {
  const [usage, setUsage] = useState<FeatureUsage | null>(null);
  useEffect(() => {
    let active = true;
    getFeatureUsage(projectId, featureId)
      .then((u) => active && setUsage(u))
      .catch(() => active && setUsage(null));
    return () => {
      active = false;
    };
  }, [projectId, featureId]);
  if (!usage || usage.roles.length === 0) return null;
  return (
    <section className="panel cost" aria-labelledby="work-order-cost">
      <div className="panel__head">
        <h2 id="work-order-cost">Cost</h2>
        <span className="muted note">
          {formatTokens(readTokens(usage.total))} in · {formatTokens(usage.total.output_tokens)} out
        </span>
      </div>
      {usage.cost && <CostParts cost={usage.cost} />}
      {(usage.phases ?? []).length > 0 && (
        <UsageTable phases={usage.phases ?? []} roles={usage.roles.map((r) => r.role)} />
      )}
    </section>
  );
}

/** What a project has used and cost so far, on its Overview. */
export function ProjectCost({
  projectId,
  onOpenOrder,
}: {
  projectId: string;
  onOpenOrder: (featureId: string) => void;
}) {
  const modelName = useModelName();
  const [usage, setUsage] = useState<ProjectUsage | null>(null);
  useEffect(() => {
    let active = true;
    getProjectUsage(projectId)
      .then((u) => active && setUsage(u))
      .catch(() => active && setUsage(null));
    return () => {
      active = false;
    };
  }, [projectId]);
  if (!usage || !anyTokens(usage.total)) return null;
  const top = usage.work_orders.slice(0, 5);
  const entry = (u: TokenUsage & { cost_usd?: number }) =>
    u.cost_usd !== undefined
      ? `≈ ${formatUSD(u.cost_usd)} · ${formatTokens(readTokens(u))} in`
      : `${formatTokens(readTokens(u))} in · ${formatTokens(u.output_tokens)} out`;
  return (
    <section className="panel cost" aria-labelledby="project-cost">
      <div className="panel__head">
        <h2 id="project-cost">Cost so far</h2>
        <span className="muted note">
          {formatTokens(readTokens(usage.total))} in · {formatTokens(usage.total.output_tokens)} out
        </span>
      </div>
      <CostParts cost={usage.cost} />
      {usage.models.length > 0 && (
        <>
          <h3 className="set__heading">By model</h3>
          <ul className="cost__list">
            {usage.models.map((m) => (
              <li key={m.model}>
                <span>{modelName(m.model)}</span>
                <span className="muted">{entry(m)}</span>
              </li>
            ))}
          </ul>
        </>
      )}
      {top.length > 0 && (
        <>
          <h3 className="set__heading">Costliest work orders</h3>
          <ul className="cost__list">
            {top.map((order) => (
              <li key={order.id}>
                <button className="linkish" onClick={() => onOpenOrder(order.id)}>
                  {order.title}
                </button>
                <span className="muted">{entry(order)}</span>
              </li>
            ))}
          </ul>
        </>
      )}
      {anyTokens(usage.assistant) && (
        <ul className="cost__list">
          <li>
            <span>Mayka&apos;s own turns</span>
            <span className="muted">{entry(usage.assistant)}</span>
          </li>
        </ul>
      )}
    </section>
  );
}
