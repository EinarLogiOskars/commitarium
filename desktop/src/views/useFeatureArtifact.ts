import { useEffect, useRef, useState } from "react";
import { getFeatureArtifact } from "../api/features";
import { streamEvents } from "../api/stream";
import { ApiError } from "../api/client";
import type { FeatureArtifact, FeatureArtifactKind } from "../api/types";

// Load a feature artifact and keep it live by following the feature event SSE
// stream: on a feature.artifact_updated for this kind at a higher revision,
// refetch. The last good value is kept across reconnects; a not-yet-created
// artifact reads as null, not an error.
export function useFeatureArtifact<T>(
  projectId: string,
  featureId: string,
  kind: FeatureArtifactKind,
  enabled = true,
): { artifact: FeatureArtifact<T> | null; error: string | null } {
  const [artifact, setArtifact] = useState<FeatureArtifact<T> | null>(null);
  const [error, setError] = useState<string | null>(null);
  const revisionRef = useRef(0);

  useEffect(() => {
    if (!enabled) return;
    let active = true;
    revisionRef.current = 0;
    setArtifact(null);
    setError(null);

    const load = async () => {
      try {
        const next = await getFeatureArtifact<T>(projectId, featureId, kind);
        if (!active || next.revision < revisionRef.current) return;
        revisionRef.current = next.revision;
        setArtifact(next);
        setError(null);
      } catch (e) {
        if (!active) return;
        // Not-yet-created is the normal early state, not a failure to surface.
        if (e instanceof ApiError && e.code === "artifact_not_found") return;
        setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
      }
    };

    void load();

    const handle = streamEvents(
      `/api/v1/projects/${encodeURIComponent(projectId)}/features/${encodeURIComponent(featureId)}/events/stream`,
      {
        onEvent: (event) => {
          let payload: { type?: string; artifact_kind?: string; artifact_revision?: number };
          try {
            payload = JSON.parse(event.data);
          } catch {
            return;
          }
          if (payload.type !== "feature.artifact_updated" || payload.artifact_kind !== kind) return;
          if (
            typeof payload.artifact_revision === "number" &&
            payload.artifact_revision <= revisionRef.current
          ) {
            return;
          }
          void load();
        },
        // Transient stream errors are non-fatal: the helper reconnects and we
        // keep showing the last good value meanwhile.
        onError: () => {},
      },
    );

    return () => {
      active = false;
      handle.close();
    };
  }, [projectId, featureId, kind, enabled]);

  return { artifact, error };
}
