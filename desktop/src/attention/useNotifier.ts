import { useEffect, useRef } from "react";
import { notifyAttention } from "../ipc";
import { planNotifications, seenIds } from "./notifications";
import type { AttentionItem } from "../api/types";

/**
 * Turns attention snapshots into native notifications.
 *
 * Deduplication is layered: this hook only considers item IDs that were absent
 * from the previous snapshot, and the native command keeps a durable ledger so
 * an item already announced stays quiet across restarts. Category preferences
 * are applied natively, so this hook only needs the master switch.
 */
export function useNotifier({
  items,
  loaded,
  foreground,
  enabled,
}: {
  items: AttentionItem[];
  loaded: boolean;
  /** The work order currently on screen, or null. */
  foreground: string | null;
  /** The user's opt-in. Off until notifications are configured in settings. */
  enabled: boolean;
}): void {
  const seen = useRef<ReadonlySet<string>>(new Set<string>());
  const primed = useRef(false);

  useEffect(() => {
    if (!loaded) return;
    const planned = enabled
      ? planNotifications({
          items,
          seen: seen.current,
          // A hidden window shows nothing, so nothing counts as foregrounded.
          foreground: document.hidden ? null : foreground,
          primed: primed.current,
        })
      : [];
    // Advance the watermark even while switched off: the native ledger only
    // records delivered events, so enabling notifications later announces what
    // happens next rather than replaying the whole backlog.
    seen.current = seenIds(items);
    primed.current = true;
    for (const notification of planned) {
      // Spelled out because the native command rejects unknown fields: the
      // navigation identifiers travel with the plan, not with the payload.
      // Delivery is best effort — a failed notification must not break polling.
      void notifyAttention({
        event_id: notification.event_id,
        kind: notification.kind,
        title: notification.title,
        body: notification.body,
      }).catch(() => {});
    }
  }, [items, loaded, enabled, foreground]);
}
