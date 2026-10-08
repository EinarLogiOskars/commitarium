import { useEffect, useRef, useState } from "react";
import { useFeatureArtifact } from "./useFeatureArtifact";
import type { AcceptanceTest, AcceptanceTestsDocument } from "../api/types";

// The reviewer's private, blind acceptance-test checklist, live over the feature
// artifact SSE. Shown during review (results) and while the reviewer authors
// them (all pending, implementation not yet pinned). Returns null until the
// artifact exists — so it only appears when independent tests are enabled.
//
// It lives in the review view's right column where there's room; the list can be
// long (one row per criterion, plus failure/n-a notes), so it collapses to a
// compact dot strip — one dot per test, colored by status — and auto-collapses
// on narrow windows. The header toggles it either way.
export function AcceptanceTestsPanel({
  projectId,
  featureId,
  enabled = true,
}: {
  projectId: string;
  featureId: string;
  enabled?: boolean;
}) {
  const { artifact } = useFeatureArtifact<AcceptanceTestsDocument>(
    projectId,
    featureId,
    "acceptance_tests",
    enabled,
  );
  // Auto-collapse on narrow windows; the user can override either way after.
  const [open, setOpen] = useState(() => !isNarrow());
  const touched = useRef(false);
  useEffect(() => {
    const mq = window.matchMedia("(max-width: 1100px)");
    const on = () => {
      if (!touched.current) setOpen(!mq.matches);
    };
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);

  if (!artifact) return null;
  const doc = artifact.document;
  const authoring = !doc.implementation_commit_id;
  const passed = doc.tests.filter((t) => t.status === "passed").length;
  const ran = doc.tests.filter((t) => t.status === "passed" || t.status === "failed").length;
  const failed = doc.tests.filter((t) => t.status === "failed").length;

  const toggle = () => {
    touched.current = true;
    setOpen((v) => !v);
  };

  return (
    <aside className={`panel accept-tests ${open ? "" : "accept-tests--collapsed"}`}>
      <button
        className="accept-tests__head"
        onClick={toggle}
        aria-expanded={open}
        title={open ? "Collapse" : "Expand"}
      >
        <h3>Independent acceptance tests</h3>
        <span className="accept-tests__count muted note">
          {failed > 0 ? `${failed} failed · ` : ""}
          {passed}/{ran || doc.tests.length} passed
        </span>
        <span className="accept-tests__chevron" aria-hidden>
          <svg viewBox="0 0 16 16" width="14" height="14" fill="none">
            <path
              d="M4 6l4 4 4-4"
              stroke="currentColor"
              strokeWidth="1.75"
              strokeLinecap="round"
              strokeLinejoin="round"
            />
          </svg>
        </span>
      </button>

      <div className="accept-tests__dots" aria-hidden>
        {doc.tests.map((t) => (
          <span key={t.id} className={`dot dot--${DOT[t.status]}`} title={`${t.title} — ${t.status}`} />
        ))}
      </div>

      {open && (
        <>
          <p className="muted note accept-tests__meta">
            plan v{doc.plan_version}
            {doc.implementation_commit_id
              ? ` · against ${doc.implementation_commit_id.slice(0, 12)}`
              : ""}
          </p>
          {authoring ? (
            <p className="muted note">
              The reviewer is preparing these privately from the plan baseline — blind to the
              implementation. Review starts once they're ready.
            </p>
          ) : (
            <p className="muted note">
              Run against the exact published commit as the first input to review — a result is
              review evidence, not an approval.
            </p>
          )}
          <ol className="accept-tests__list">
            {doc.tests.map((t) => (
              <AcceptanceRow key={t.id} t={t} />
            ))}
          </ol>
        </>
      )}
    </aside>
  );
}

function isNarrow(): boolean {
  try {
    return window.matchMedia("(max-width: 1100px)").matches;
  } catch {
    return false;
  }
}

const MARK: Record<AcceptanceTest["status"], { ch: string; tone: string; label: string }> = {
  pending: { ch: "○", tone: "muted", label: "pending" },
  running: { ch: "◐", tone: "warn", label: "running" },
  passed: { ch: "✓", tone: "ok", label: "passed" },
  failed: { ch: "✗", tone: "bad", label: "failed" },
  not_applicable: { ch: "–", tone: "muted", label: "n/a" },
};

// Dot color per status: gray pending/n-a, amber running, green passed, red failed.
const DOT: Record<AcceptanceTest["status"], string> = {
  pending: "muted",
  running: "warn",
  passed: "ok",
  failed: "bad",
  not_applicable: "muted",
};

function AcceptanceRow({ t }: { t: AcceptanceTest }) {
  const m = MARK[t.status];
  return (
    <li className={`accept-test accept-test--${t.status}`}>
      <div className="accept-test__line">
        <span
          className={`accept-test__mark state--${m.tone}${t.status === "running" ? " accept-test__mark--run" : ""}`}
          aria-hidden
        >
          {m.ch}
        </span>
        <span className="accept-test__title">{t.title}</span>
        <span className={`muted accept-test__status accept-test__status--${m.tone}`}>{m.label}</span>
      </div>
      {t.note && (t.status === "failed" || t.status === "not_applicable") && (
        <p className="accept-test__note">{t.note}</p>
      )}
    </li>
  );
}
