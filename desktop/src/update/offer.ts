// A new version is offered once, in a dialog, when it is found. Closing it is
// "Later": that version is not offered again, and the rail keeps the way back.

const KEY = "commitarium.update.offered";

export function shouldOffer(
  status: string,
  version: string | undefined,
  running: number,
  offered: string | null,
): boolean {
  // Not while agents work: it waits for a quiet moment.
  return status === "available" && !!version && running === 0 && offered !== version;
}

export function offeredVersion(): string | null {
  try {
    return localStorage.getItem(KEY);
  } catch {
    return null;
  }
}

export function markOffered(version: string) {
  try {
    localStorage.setItem(KEY, version);
  } catch {
    // Without storage it is offered again next launch.
  }
}
