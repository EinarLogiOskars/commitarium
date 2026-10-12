import type { AcceptanceTest, AcceptanceTestsDocument } from "../api/types";

// The reviewer's private, blind acceptance-test checklist, shown in the work
// order's side card from review on (see OrderSidebar).

const MARK: Record<AcceptanceTest["status"], { ch: string; tone: string; label: string }> = {
  pending: { ch: "○", tone: "muted", label: "pending" },
  running: { ch: "◐", tone: "warn", label: "running" },
  passed: { ch: "✓", tone: "ok", label: "passed" },
  failed: { ch: "✗", tone: "bad", label: "failed" },
  not_applicable: { ch: "–", tone: "muted", label: "n/a" },
};

// Dot color per status: gray pending/n-a, amber running, green passed, red failed.
export const DOT: Record<AcceptanceTest["status"], string> = {
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
        <div className="accept-test__body">
          <span className="accept-test__title">{t.title}</span>
          <span className="accept-test__meta">
            <span className={`accept-test__status accept-test__status--${m.tone}`}>
              Reviewer: {m.label}
            </span>
            {t.coordinator_status && (
              <span
                className={`accept-test__own state--${t.coordinator_status === "passed" ? "ok" : "bad"}`}
                title="Commitarium's own run of this test, by its exit code, apart from the reviewer"
              >
                Commitarium: {t.coordinator_status}
              </span>
            )}
          </span>
        </div>
      </div>
      {t.note && (t.status === "failed" || t.status === "not_applicable") && (
        <p className="accept-test__note">{t.note}</p>
      )}
    </li>
  );
}

/** The acceptance tests' content, for the work order's side card. */
export function AcceptanceTestsList({ doc }: { doc: AcceptanceTestsDocument }) {
  const authoring = !doc.implementation_commit_id;
  return (
    <>
      <p className="muted note accept-tests__meta">
        plan v{doc.plan_version}
        {doc.implementation_commit_id
          ? ` · against ${doc.implementation_commit_id.slice(0, 12)}`
          : ""}
        {doc.coordinator_commit_id
          ? ` · Commitarium ran them against ${doc.coordinator_commit_id.slice(0, 12)}`
          : ""}
      </p>
      <p className="muted note">
        {authoring
          ? "The reviewer is preparing these privately from the plan baseline — blind to the implementation. Review starts once they're ready."
          : "Run against the exact published commit as the first input to review — a result is review evidence, not an approval."}
      </p>
      <ol className="accept-tests__list">
        {doc.tests.map((t) => (
          <AcceptanceRow key={t.id} t={t} />
        ))}
      </ol>
    </>
  );
}
