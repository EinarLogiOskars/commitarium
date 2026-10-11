// Which session replies the user has seen (ADR-019): a per-viewer convenience
// kept in browser storage, so a session with a newer reply shows as new.

const KEY = "commitarium.session-seen.";
const EVENT = "commitarium-session-seen";

/** When the user last saw the session's latest reply, if ever. */
export function sessionSeenAt(featureId: string): string | null {
  try {
    return localStorage.getItem(KEY + featureId);
  } catch {
    return null;
  }
}

/** Records that the user has seen the session as of `at`. */
export function markSessionSeen(featureId: string, at: string): void {
  try {
    if (localStorage.getItem(KEY + featureId) === at) return;
    localStorage.setItem(KEY + featureId, at);
  } catch {
    return;
  }
  window.dispatchEvent(new Event(EVENT));
}

/** Calls `handler` whenever a session is marked seen. */
export function onSessionSeen(handler: () => void): () => void {
  window.addEventListener(EVENT, handler);
  return () => window.removeEventListener(EVENT, handler);
}
