import { useEffect, useMemo, useState } from "react";
import { getSessionEvents } from "../api/sessions";
import { streamEvents } from "../api/stream";
import { ApiError } from "../api/client";
import type { MessagePreview, SessionEvent } from "../api/types";
import type { TranscriptEntry } from "./Transcript";

export interface SessionRef {
  id: string;
  role: string; // "lead" | "reviewer" | agent id
}

// Live transcript for one or more sessions. Seeds durable history once per
// session, then follows each session's SSE: durable frames (deduped by event id)
// build the transcript, and transient `message_preview` frames drive live
// streaming bubbles that a matching durable event later replaces.
//
// Durable entries come back sorted by time; previews come back separately so a
// caller can append them after phase-scoping (a preview is the current turn and
// should always show, regardless of the historical window filter).
export function useSessionEvents(
  sessions: SessionRef[],
  enabled = true,
): { durable: TranscriptEntry[]; previews: TranscriptEntry[]; error: string | null } {
  const [durableById, setDurableById] = useState<Record<string, TranscriptEntry>>({});
  const [previewByKey, setPreviewByKey] = useState<Record<string, TranscriptEntry>>({});
  const [error, setError] = useState<string | null>(null);

  // Stable identity for the session set so the effect only re-subscribes when it
  // actually changes (e.g. the reviewer session appears mid-run).
  const key = sessions
    .map((s) => `${s.id}:${s.role}`)
    .sort()
    .join(",");

  useEffect(() => {
    if (!enabled || sessions.length === 0) return;
    let active = true;
    setDurableById({});
    setPreviewByKey({});
    setError(null);

    const roleFor = (role: string, type: string) => (type === "user_message" ? "user" : role);
    const previewKey = (sessionId: string, streamId: string) => `${sessionId}::${streamId}`;

    const addDurable = (sessionId: string, role: string, e: SessionEvent) => {
      setDurableById((cur) => ({
        ...cur,
        [e.id]: {
          key: e.id,
          role: roleFor(role, e.type),
          type: e.type,
          text: e.text,
          activity: e.activity,
          at: e.occurred_at,
        },
      }));
      // A final message supersedes its live preview.
      if (e.stream_id) {
        const k = previewKey(sessionId, e.stream_id);
        setPreviewByKey((cur) => {
          if (!(k in cur)) return cur;
          const next = { ...cur };
          delete next[k];
          return next;
        });
      }
    };

    const handles = sessions.map((s) => {
      // Seed durable history once.
      getSessionEvents(s.id)
        .then((events) => {
          if (!active) return;
          for (const e of events as SessionEvent[]) addDurable(s.id, s.role, e);
        })
        .catch((e) => active && setError(describe(e)));

      // Follow the live stream: durable events + transient previews.
      return streamEvents(`/api/v1/sessions/${encodeURIComponent(s.id)}/events/stream`, {
        onEvent: (frame) => {
          if (!active) return;
          if (frame.event === "message_preview") {
            let p: MessagePreview;
            try {
              p = JSON.parse(frame.data);
            } catch {
              return;
            }
            setPreviewByKey((cur) => ({
              ...cur,
              [previewKey(s.id, p.stream_id)]: {
                key: `preview:${previewKey(s.id, p.stream_id)}`,
                role: s.role,
                type: "message",
                text: p.text,
                at: PREVIEW_AT, // sorts after all durable events (current turn)
                streaming: true,
              },
            }));
            return;
          }
          let e: SessionEvent;
          try {
            e = JSON.parse(frame.data);
          } catch {
            return;
          }
          addDurable(s.id, s.role, e);
        },
        onError: () => {}, // transient; helper reconnects, last good state stays
      });
    });

    return () => {
      active = false;
      for (const h of handles) h.close();
    };
  }, [key, enabled]);

  const durable = useMemo(
    () => Object.values(durableById).sort((a, b) => a.at.localeCompare(b.at)),
    [durableById],
  );
  const previews = useMemo(() => Object.values(previewByKey), [previewByKey]);

  return { durable, previews, error };
}

// Far-future sentinel so preview bubbles always sort last (they are the
// in-flight current turn). Never rendered as a timestamp.
const PREVIEW_AT = "9999-12-31T23:59:59.999Z";

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}
