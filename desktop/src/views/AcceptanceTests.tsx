import { useFeatureArtifact } from "./useFeatureArtifact";
import type { AcceptanceTest, AcceptanceTestsDocument } from "../api/types";

// The reviewer's private, blind acceptance-test checklist, live over the feature
// artifact SSE. Shown during review (results) and while the reviewer authors
// them (all pending, implementation not yet pinned). Returns null until the
// artifact exists — so it only appears when independent tests are enabled.
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
  if (!artifact) return null;
  const doc = artifact.document;
  const authoring = !doc.implementation_commit_id;
  const passed = doc.tests.filter((t) => t.status === "passed").length;
  const ran = doc.tests.filter((t) => t.status === "passed" || t.status === "failed").length;

  return (
    <section className="panel accept-tests">
      <div className="panel__head">
        <h3>Independent acceptance tests</h3>
        <span className="muted note">
          plan v{doc.plan_version}
          {doc.implementation_commit_id ? ` · against ${doc.implementation_commit_id.slice(0, 12)}` : ""}
        </span>
      </div>
      {authoring ? (
        <p className="muted note">
          The reviewer is preparing these privately from the plan baseline — blind to the
          implementation. Review starts once they're ready.
        </p>
      ) : (
        <p className="muted note">
          Run against the exact published commit as the first input to review — a result is review
          evidence, not an approval. {passed}/{ran || doc.tests.length} passed.
        </p>
      )}
      <ol className="accept-tests__list">
        {doc.tests.map((t) => (
          <AcceptanceRow key={t.id} t={t} />
        ))}
      </ol>
    </section>
  );
}

const MARK: Record<AcceptanceTest["status"], { ch: string; tone: string; label: string }> = {
  pending: { ch: "○", tone: "muted", label: "pending" },
  running: { ch: "◐", tone: "warn", label: "running" },
  passed: { ch: "✓", tone: "ok", label: "passed" },
  failed: { ch: "✗", tone: "bad", label: "failed" },
  not_applicable: { ch: "–", tone: "muted", label: "n/a" },
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
