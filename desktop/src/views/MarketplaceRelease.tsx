import { useEffect, useState } from "react";
import { ApiError } from "../api/client";
import { getReleaseStatus, releaseApp, type ReleaseStatus } from "../api/releases";

// An app's place on the marketplace: its latest version and the Release
// button. Hidden when the coordinator has no marketplace.
export function MarketplaceRelease({ projectId }: { projectId: string }) {
  const [status, setStatus] = useState<ReleaseStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    getReleaseStatus(projectId)
      .then(setStatus)
      .catch(() => setStatus(null));
  }, [projectId]);

  if (!status) return null;
  const latest = status.latest;

  const release = async () => {
    setBusy(true);
    setError(null);
    try {
      const released = await releaseApp(projectId);
      setStatus({ latest: released, unreleased: false });
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="panel dash__release">
      <div>
        <h2>Marketplace</h2>
        <p className="muted">
          {latest
            ? `${latest.version} released ${new Date(latest.released_at).toLocaleDateString()} by ${latest.released_by}. ` +
              (status.unreleased ? "The app has changed since." : "Up to date.")
            : "Not released yet. Releasing shares the app with the people on your marketplace."}
        </p>
        {error && <div className="banner banner--error">{error}</div>}
      </div>
      <button
        className={status.unreleased ? "primary" : ""}
        disabled={busy || !status.unreleased}
        onClick={() => void release()}
      >
        {busy ? "Releasing…" : latest ? "Release an update" : "Release"}
      </button>
    </section>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === "marketplace_not_signed_in")
      return "Sign in to the marketplace in Settings to release apps.";
    return e.message;
  }
  return String(e);
}
