import { useEffect, useMemo, useRef, useState } from "react";
import { Transcript } from "./Transcript";
import { InterveneBar } from "./InterveneBar";
import { Markdown } from "./Markdown";
import { useFeatureArtifact } from "./useFeatureArtifact";
import { useSessionEvents, type SessionRef } from "./useSessionEvents";
import { scopeToPhase, type Interval } from "./phaseWindows";
import type { ImplementationPlanDocument, PlanStep, Run } from "../api/types";

const SHOWN = new Set(["message", "activity"]);

// Implementation phase: the lead writes the code per the agreed plan. The
// checklist sidebar tracks the durable implementation_plan artifact — each
// commit-sized step advances pending → in_progress → completed live (no user
// gate between commits). The transcript shows the lead's activity.
export function ImplementationView({
  projectId,
  featureId,
  run,
  live = true,
  intervals = [],
  scoped = false,
  suppressRecover = false,
  onChanged,
}: {
  projectId: string;
  featureId: string;
  runId: string;
  run: Run | null;
  live?: boolean;
  intervals?: Interval[];
  scoped?: boolean;
  suppressRecover?: boolean;
  onChanged: () => void;
}) {
  const feedRef = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);

  const { artifact: plan } = useFeatureArtifact<ImplementationPlanDocument>(
    projectId,
    featureId,
    "implementation_plan",
  );

  // Only the lead acts during implementation; stream its transcript live.
  const sessions = useMemo<SessionRef[]>(() => {
    const lead = run?.sessions.find((s) => s.role === "lead") ?? run?.sessions[0];
    return lead ? [{ id: lead.id, role: "lead" }] : [];
  }, [run]);
  const { durable, previews, error } = useSessionEvents(sessions);

  const shown = useMemo(() => {
    const scopedDurable = scopeToPhase(
      durable.filter((e) => e.text && SHOWN.has(e.type)),
      intervals,
      scoped,
    );
    return [...scopedDurable, ...previews];
  }, [durable, previews, intervals, scoped]);

  useEffect(() => {
    const el = feedRef.current;
    if (el && pinned.current) el.scrollTop = el.scrollHeight;
  }, [shown]);

  return (
    <section className="panel panel--phase">
      <h2>Implementation</h2>
      {error && <div className="banner banner--error">{error}</div>}

      <div className="impl">
        <aside className="impl__side">
          <PlanChecklist plan={plan?.document ?? null} />
        </aside>
        <div className="impl__main">
          <div
            className="chat"
            ref={feedRef}
            onScroll={(e) => {
              const el = e.currentTarget;
              pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
            }}
          >
            <Transcript entries={shown} empty="Waiting for the lead to start…" />
          </div>

          {live && run && (
            <InterveneBar run={run} onChanged={onChanged} suppressRecover={suppressRecover} />
          )}
        </div>
      </div>
    </section>
  );
}

function PlanChecklist({ plan }: { plan: ImplementationPlanDocument | null }) {
  // One step open at a time (accordion). Auto-follows the active step until the
  // user picks a step themselves, after which their choice is respected.
  const [openId, setOpenId] = useState<string | null>(null);
  const touched = useRef(false);
  const activeId = plan?.steps.find((s) => s.status === "in_progress")?.id ?? null;
  useEffect(() => {
    if (!touched.current) setOpenId(activeId);
  }, [activeId]);
  const toggle = (id: string) => {
    touched.current = true;
    setOpenId((cur) => (cur === id ? null : id));
  };

  if (!plan) {
    return (
      <div className="plan">
        <h3>Plan</h3>
        <p className="muted note">The implementation plan will appear here once it's agreed.</p>
      </div>
    );
  }
  const done = plan.steps.filter((s) => s.status === "completed").length;
  return (
    <div className="plan">
      <div className="plan__head">
        <h3>{plan.title}</h3>
        {plan.subtitle && <p className="muted plan__subtitle">{plan.subtitle}</p>}
        <span className="muted note">
          {done}/{plan.steps.length} steps · plan v{plan.plan_version}
        </span>
      </div>
      <ol className="plan__steps">
        {plan.steps.map((step) => (
          <PlanStepRow
            key={step.id}
            step={step}
            open={openId === step.id}
            onToggle={() => toggle(step.id)}
          />
        ))}
      </ol>
    </div>
  );
}

const STATUS_MARK: Record<PlanStep["status"], string> = {
  pending: "○",
  in_progress: "◐",
  completed: "✓",
};

function PlanStepRow({
  step,
  open,
  onToggle,
}: {
  step: PlanStep;
  open: boolean;
  onToggle: () => void;
}) {
  const hasDetail =
    Boolean(step.details_markdown) ||
    (step.verification?.length ?? 0) > 0 ||
    Boolean(step.commit_subject) ||
    Boolean(step.commit_id);
  const rowRef = useRef<HTMLLIElement | null>(null);

  // On expand, bring the step's top into view so the title and start of its
  // detail are visible — otherwise expanding the last item leaves the checklist
  // scrolled to the end of the detail.
  useEffect(() => {
    if (open) rowRef.current?.scrollIntoView({ block: "start", behavior: "smooth" });
  }, [open]);

  return (
    <li ref={rowRef} className={`plan-step plan-step--${step.status}`}>
      <button
        className="plan-step__row"
        onClick={() => hasDetail && onToggle()}
        aria-expanded={hasDetail ? open : undefined}
      >
        <span className="plan-step__mark" aria-hidden>
          {STATUS_MARK[step.status]}
        </span>
        <span className="plan-step__text">
          <span className="plan-step__title">{step.title}</span>
          {step.subtitle && <span className="muted plan-step__subtitle">{step.subtitle}</span>}
        </span>
        {hasDetail && <span className="plan-step__chevron">{open ? "▼" : "▶"}</span>}
      </button>
      {open && hasDetail && (
        // Clicking anywhere in the expanded body collapses it too.
        <div className="plan-step__detail" onClick={onToggle}>
          {step.details_markdown && <Markdown text={step.details_markdown} />}
          {step.commit_subject && (
            <p className="muted note">
              Commit: <span className="mono">{step.commit_subject}</span>
            </p>
          )}
          {step.verification && step.verification.length > 0 && (
            <div className="plan-step__verify">
              <span className="muted note">Verification</span>
              <ul>
                {step.verification.map((v, i) => (
                  <li key={i} className="mono">
                    {v}
                  </li>
                ))}
              </ul>
            </div>
          )}
          {step.commit_id && (
            <p className="muted note">
              Committed <span className="mono">{step.commit_id.slice(0, 12)}</span>
            </p>
          )}
        </div>
      )}
    </li>
  );
}
