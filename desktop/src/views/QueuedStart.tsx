import { useState } from "react";
import type { Feature } from "../api/types";

// Start for a work order the assistant queued behind other work: it asks
// first, since it builds on that work, and keeping it queued
// is the default. Starting it anyway takes it out of the queue.
export function QueuedStart({
  feature,
  busy,
  onStart,
}: {
  feature: Feature;
  busy: boolean;
  onStart: () => void;
}) {
  const [asking, setAsking] = useState(false);
  if (asking && feature.queued_after) {
    return (
      <div className="queued-start">
        <p>
          This builds on “{feature.queued_after}”, which is still being built. Starting now may cost
          extra work once that one lands.
        </p>
        <div className="row">
          <button className="primary" autoFocus onClick={() => setAsking(false)}>
            Keep it queued
          </button>
          <button
            className="ghost"
            onClick={() => {
              setAsking(false);
              onStart();
            }}
          >
            Start anyway
          </button>
        </div>
      </div>
    );
  }
  return (
    <button
      className="primary"
      onClick={() => (feature.queued_after ? setAsking(true) : onStart())}
      disabled={busy}
    >
      {busy ? "Starting…" : "Start"}
    </button>
  );
}

/** The note on a queued work order's page: what it waits for. */
export function QueuedNote({ feature }: { feature: Feature }) {
  if (!feature.queued_after) return null;
  return (
    <p className="muted note">
      Queued: the assistant starts it once “{feature.queued_after}” has merged.
    </p>
  );
}
