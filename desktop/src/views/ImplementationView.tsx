import { useCallback, useEffect, useRef, useState } from "react";
import { getRun } from "../api/runs";
import { getSessionEvents } from "../api/sessions";
import { ApiError } from "../api/client";
import { Transcript, type TranscriptEntry } from "./Transcript";
import { InterveneBar } from "./InterveneBar";
import { Markdown } from "./Markdown";
import { useFeatureArtifact } from "./useFeatureArtifact";
import { scopeToPhase, type Interval } from "./phaseWindows";
import type { ImplementationPlanDocument, PlanStep, Run, SessionEvent } from "../api/types";

const POLL_MS = 2000;

const SHOWN = new Set(["message", "activity"]);

// Implementation phase: the lead writes the code per the agreed plan. The
// checklist sidebar tracks the durable implementation_plan artifact — each
// commit-sized step advances pending → in_progress → completed live (no user
// gate between commits). The transcript shows the lead's activity.
export function ImplementationView({
  projectId,
  featureId,
  runId,
  run,
  live = true,
  intervals = [],
  scoped = false,
  onChanged,
}: {
  projectId: string;
  featureId: string;
  runId: string;
  run: Run | null;
  live?: boolean;
  intervals?: Interval[];
  scoped?: boolean;
  onChanged: () => void;
}) {
  const [entries, setEntries] = useState<TranscriptEntry[]>([]);
  const [error, setError] = useState<string | null>(null);
  const feedRef = useRef<HTMLDivElement | null>(null);
  const pinned = useRef(true);

  const { artifact: plan } = useFeatureArtifact<ImplementationPlanDocument>(
    projectId,
    featureId,
    "implementation_plan",
  );

  const poll = useCallback(async () => {
    try {
      const run = await getRun(runId);
      const lead = run.sessions.find((s) => s.role === "lead") ?? run.sessions[0];
      if (!lead) return;
      const events = (await getSessionEvents(lead.id)) as SessionEvent[];
      setEntries(
        events
          .filter((e) => e.text && SHOWN.has(e.type))
          .map((e) => ({ key: e.id, role: "lead", type: e.type, text: e.text, activity: e.activity, at: e.occurred_at })),
      );
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    }
  }, [runId]);

  useEffect(() => {
    void poll();
    const id = setInterval(() => void poll(), POLL_MS);
    return () => clearInterval(id);
  }, [poll]);

  useEffect(() => {
    const el = feedRef.current;
    if (el && pinned.current) el.scrollTop = el.scrollHeight;
  }, [entries]);

  const shown = scopeToPhase(entries, intervals, scoped);

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

          {live && run && <InterveneBar run={run} onChanged={onChanged} />}
        </div>
      </div>
    </section>
  );
}

function PlanChecklist({ plan }: { plan: ImplementationPlanDocument | null }) {
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
          <PlanStepRow key={step.id} step={step} />
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

function PlanStepRow({ step }: { step: PlanStep }) {
  const [open, setOpen] = useState(step.status === "in_progress");
  const hasDetail =
    Boolean(step.details_markdown) ||
    (step.verification?.length ?? 0) > 0 ||
    Boolean(step.commit_subject) ||
    Boolean(step.commit_id);

  return (
    <li className={`plan-step plan-step--${step.status}`}>
      <button
        className="plan-step__row"
        onClick={() => hasDetail && setOpen((v) => !v)}
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
        <div className="plan-step__detail">
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
