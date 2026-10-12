import { useEffect, useRef, useState } from "react";
import { useFeatureArtifact } from "./useFeatureArtifact";
import { PlanChecklist, STEP_DOT } from "./ImplementationView";
import { AcceptanceTestsList, DOT } from "./AcceptanceTests";
import type { AcceptanceTestsDocument, ImplementationPlanDocument } from "../api/types";

type Tab = "checklist" | "tests" | "details";

const TAB_LABELS: Record<Tab, string> = {
  checklist: "Checklist",
  tests: "Tests",
  details: "Details",
};

// The work order's one side card, beside the conversation. Its tabs are only
// what the viewed phase needs: the implementation checklist while the lead
// implements, the acceptance tests from review on, and the work order's
// description whenever it has one. It folds into a slim strip showing the
// current work's progress.
export function OrderSidebar({
  projectId,
  featureId,
  phase,
  description,
  detailsRequest,
}: {
  projectId: string;
  featureId: string;
  /** The viewed phase's index: 2 is Implement, 3 Review, 4 Merge. */
  phase: number;
  description?: string;
  /** Bumped by the header's Details button to open the Details tab. */
  detailsRequest: number;
}) {
  const { artifact: plan } = useFeatureArtifact<ImplementationPlanDocument>(
    projectId,
    featureId,
    "implementation_plan",
    phase === 2,
  );
  const { artifact: tests } = useFeatureArtifact<AcceptanceTestsDocument>(
    projectId,
    featureId,
    "acceptance_tests",
    phase >= 3,
  );
  const checklist = phase === 2 ? (plan?.document ?? null) : null;
  const testDoc = phase >= 3 ? (tests?.document ?? null) : null;
  const tabs: Tab[] = [
    ...(checklist ? (["checklist"] as const) : []),
    ...(testDoc ? (["tests"] as const) : []),
    ...(description ? (["details"] as const) : []),
  ];
  const work = checklist || testDoc;

  const [open, setOpen] = useState(!!work);
  const [tab, setTab] = useState<Tab | null>(null);
  const touched = useRef(false);

  // A phase with work to follow opens on it, unless the user chose otherwise.
  useEffect(() => {
    if (!touched.current) setOpen(!!work);
    setTab(null);
  }, [phase, !!work]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (detailsRequest === 0) return;
    touched.current = true;
    setTab("details");
    setOpen(true);
  }, [detailsRequest]);

  if (tabs.length === 0) return null;
  const active: Tab = tab && tabs.includes(tab) ? tab : tabs[0];

  // With only the description, the header's Details button is the way in.
  if (!open && !work) return null;
  if (!open) {
    const steps = checklist?.steps ?? [];
    const results = testDoc?.tests ?? [];
    return (
      <button
        className="order-side__strip"
        onClick={() => {
          touched.current = true;
          setOpen(true);
        }}
        title={`Show the ${TAB_LABELS[tabs[0]].toLowerCase()}`}
      >
        <span className="order-side__chevron" aria-hidden>
          ‹
        </span>
        {checklist && (
          <>
            <span className="muted note">
              {steps.filter((s) => s.status === "completed").length}/{steps.length}
            </span>
            <span className="order-side__dots" aria-hidden>
              {steps.map((s) => (
                <span key={s.id} className={`dot dot--${STEP_DOT[s.status] ?? "muted"}`} />
              ))}
            </span>
          </>
        )}
        {testDoc && (
          <>
            <span className="muted note">
              {results.filter((t) => t.status === "passed").length}/{results.length}
            </span>
            <span className="order-side__dots" aria-hidden>
              {results.map((t) => (
                <span key={t.id} className={`dot dot--${DOT[t.status]}`} />
              ))}
            </span>
          </>
        )}
      </button>
    );
  }

  return (
    <aside className="panel order-side">
      <div className="order-side__head">
        <div className="order-side__tabs" role="tablist">
          {tabs.map((t) => (
            <button
              key={t}
              role="tab"
              aria-selected={t === active}
              className={`order-side__tab ${t === active ? "order-side__tab--on" : ""}`}
              onClick={() => {
                touched.current = true;
                setTab(t);
              }}
            >
              {TAB_LABELS[t]}
            </button>
          ))}
        </div>
        <button
          className="order-side__chevron"
          onClick={() => {
            touched.current = true;
            setOpen(false);
          }}
          title="Fold away"
          aria-label="Fold away"
        >
          ›
        </button>
      </div>
      <div className="order-side__body">
        {active === "checklist" && checklist && <PlanChecklist plan={checklist} />}
        {active === "tests" && testDoc && <AcceptanceTestsList doc={testDoc} />}
        {active === "details" && <p className="order-side__details">{description}</p>}
      </div>
    </aside>
  );
}
