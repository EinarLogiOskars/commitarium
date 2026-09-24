import type { AttentionItem, AttentionKind } from "../api/types";
import type { NativeNotification } from "../ipc";

// Which attention kinds are worth interrupting for, and how the native layer
// should categorize them. Everything absent here is deliberately quiet:
// `validation_pending` / `validation_running` / `environment_provisioning` are
// ordinary progress, and `paused` is something the user did themselves.
const NOTIFY: Partial<Record<AttentionKind, NativeNotification["kind"]>> = {
  clarification: "attention",
  phase_checkpoint: "attention",
  round_cap: "attention",
  blocker: "attention",
  merge_approval: "attention",
  environment_approval: "attention",
  environment_failed: "failure",
  validation_failed: "failure",
  run_failed: "failure",
  auto_merge_completed: "auto_merge",
};

/** A notification to hand to the native layer, plus where it should navigate. */
export interface PlannedNotification extends NativeNotification {
  project_id: string;
  feature_id: string;
}

export interface PlanInput {
  /** The newest coordinator snapshot. */
  items: AttentionItem[];
  /** Item IDs from the previous snapshot; only unseen IDs can notify. */
  seen: ReadonlySet<string>;
  /**
   * The work order on screen in a focused window, if any. Its transitions are
   * visible as they happen, so they don't also need an OS notification.
   */
  foreground: string | null;
  /**
   * False until the first snapshot has been absorbed. At launch the inbox
   * already shows everything outstanding, so the opening state is adopted
   * silently; notifications are for what changes while the user isn't looking.
   */
  primed: boolean;
}

/**
 * Decides what to notify about, given two consecutive snapshots. Pure: the
 * caller owns the seen-set, the native call and the durable ledger behind it.
 *
 * Attention item IDs already encode their transition — a run's wait carries its
 * `updated_at`, and an environment request its status and `updated_at` — so an
 * unchanged state keeps the same ID and cannot re-notify, while a genuine
 * re-entry (a retried provision failing again) produces a new one that can.
 */
export function planNotifications(input: PlanInput): PlannedNotification[] {
  if (!input.primed) return [];
  const planned: PlannedNotification[] = [];
  const emitted = new Set<string>();
  for (const item of input.items) {
    const kind = NOTIFY[item.kind];
    if (!kind) continue;
    if (input.seen.has(item.id)) continue;
    if (input.foreground && item.feature_id === input.foreground) continue;
    if (emitted.has(item.id)) continue;
    emitted.add(item.id);
    planned.push({
      event_id: item.id,
      kind,
      title: clamp(`${item.title} · ${item.project_name}`, 120, "Commitarium"),
      body: clamp(`${item.feature_title} — ${item.detail}`, 500, item.feature_title),
      project_id: item.project_id,
      feature_id: item.feature_id,
    });
  }
  return planned;
}

const bytes = new TextEncoder();

// The native command rejects empty or over-long text outright, and measures its
// limits in bytes — so clamp here rather than letting a long project name or a
// provisioning error with non-ASCII text fail delivery.
function clamp(value: string, maximum: number, fallback: string): string {
  let text = value.trim() || fallback;
  if (bytes.encode(text).length <= maximum) return text;
  // The ellipsis costs 3 bytes; shrink by characters until the whole fits.
  let end = text.length;
  do {
    end -= 1;
    text = `${value.trim().slice(0, end)}…`;
  } while (end > 0 && bytes.encode(text).length > maximum);
  return text;
}

/** The seen-set to carry into the next comparison. */
export function seenIds(items: AttentionItem[]): Set<string> {
  return new Set(items.map((item) => item.id));
}
