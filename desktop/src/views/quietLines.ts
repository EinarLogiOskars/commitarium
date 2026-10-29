import type { AssistantAction } from "../api/assistant";
import { appliedWhat } from "./actionCard";
import type { PlanChange } from "./planMode";
import type { TurnPiece } from "./appliedSummary";

// The chat's quiet lines: Commitarium's event notes ("The brief for “Sort”
// is ready.") and what Mayka set going ("✓ Accepted the brief: Sort"). They
// share one look, left under the messages with a glyph that says how each
// went; a run of routine ones between two messages folds into one summary
// row ("✓ Accepted 3 briefs · started 3 work orders ▸"), while those that
// need the user stay in view. Lines that repeat another are dropped.

/** How a line went: its glyph, and whether it may fold away. */
export type QuietTone = "done" | "info" | "busy" | "needs" | "failed";

/** What a line is about, for a folded run's summary. */
export type QuietKind =
  | "brief"
  | "brief-ready"
  | "brief-write"
  | "created"
  | "started"
  | "work"
  | "plan"
  | "research"
  | "autopilot"
  | "merged"
  | "vision"
  | "stack"
  | "answer"
  | "order"
  | "note";

export interface QuietLine {
  tone: QuietTone;
  kind: QuietKind;
  /** How many of its kind it stands for (a turn's summary of work orders). */
  count: number;
  text: string;
  /** Stays in view even though it is routine (the latest written plan). */
  pinned?: boolean;
  /** For a "The brief for X is ready." note: X, lowercased. */
  briefFor?: string;
  /** For an accepted brief: its work order's title, lowercased. */
  accepts?: string;
  /** For update_plan: it drafted or revised the written plan (it links to it). */
  planDocument?: boolean;
}

/** A chat row: a quiet line, or anything else (a message, a card), which
 * stays as it is and splits the runs around it. */
export type QuietEntry<T> = { quiet: QuietLine; node: T } | { block: T };

export type QuietSegment<T> =
  | { kind: "block"; node: T }
  | { kind: "line"; line: QuietLine; node: T }
  | { kind: "run"; lines: { line: QuietLine; node: T }[]; summary: string; tone: QuietTone };

/** A line that needs the user, failed, or is still under way never folds. */
export function staysInView(line: QuietLine): boolean {
  return !!line.pinned || (line.tone !== "done" && line.tone !== "info");
}

const BRIEF_READY = /^The brief for “(.+)” is ready\.$/;
const NEEDS_YOU =
  /waiting for the user|waits for (the user|your)|needs you|for you[:.]|yours to approve|\basks: |is blocked/i;
const FAILED = /\b(failed|could not|couldn't|stopped before it finished|did not hold)\b/i;

/** A Commitarium note in the chat, as a quiet line. */
export function eventLine(text: string): QuietLine {
  const brief = BRIEF_READY.exec(text);
  if (brief) {
    return { tone: "info", kind: "brief-ready", count: 1, text, briefFor: brief[1].toLowerCase() };
  }
  if (NEEDS_YOU.test(text)) return { tone: "needs", kind: "note", count: 1, text };
  if (FAILED.test(text)) return { tone: "failed", kind: "note", count: 1, text };
  if (/^Autopilot/.test(text)) {
    return {
      tone: /^Autopilot finished/.test(text) ? "done" : "info",
      kind: "autopilot",
      count: 1,
      text,
    };
  }
  if (/(?<!has) merged\.$/.test(text)) return { tone: "done", kind: "merged", count: 1, text };
  return { tone: "info", kind: "note", count: 1, text };
}

/** What Mayka set going, said in a line (without its glyph). */
export function appliedLine(
  action: AssistantAction,
  change?: PlanChange,
  running?: boolean,
): QuietLine {
  const done = action.status === "applied";
  const line = (tone: QuietTone, kind: QuietKind, text: string): QuietLine => ({
    tone,
    kind,
    count: 1,
    text,
  });
  const workKind: QuietKind =
    action.type === "create_work_order"
      ? action.hold_start || action.brief
        ? "created"
        : "started"
      : action.type === "start_work_order"
        ? "started"
        : "work";
  if (action.settled) {
    // "Already started: Add a beginner glossary"
    const why = (action.result ?? "").split(":")[0].replace(/\.$/, "") || "No longer needed";
    return line("done", "work", `${why}: ${appliedWhat(action.title) || "the work order"}`);
  }
  switch (action.type) {
    case "answer_helper":
      return line(
        done ? "done" : "busy",
        "answer",
        `${done ? "Answered" : "Answering"}: ${action.guidance ?? ""}`,
      );
    case "update_plan":
      if (change?.document && done) {
        return {
          ...line("done", "plan", change.document === "drafted" ? "Plan drafted" : "Plan revised"),
          planDocument: true,
        };
      }
      return line(done ? "done" : "busy", "plan", done ? "Updated the plan" : "Updating the plan…");
    case "research":
      // Research runs in the background: while it does, the line says so
      // and that the user can carry on, instead of a tick that reads as done.
      if (running) {
        return line(
          "busy",
          "research",
          `Looking up: ${action.title}… You can keep answering meanwhile.`,
        );
      }
      return line(
        done ? "done" : "busy",
        "research",
        `${done ? "Looked up" : "Looking up"}: ${action.title}`,
      );
    case "design_stack":
      return line(
        done ? "done" : "busy",
        "stack",
        done ? "Designing the stack" : "Designing the stack…",
      );
    case "apply_stack":
      return line(
        done ? "done" : "busy",
        "stack",
        done ? "Set up the stack" : "Setting up the stack…",
      );
    case "accept_brief":
      return {
        ...line(
          done ? "done" : "busy",
          "brief",
          `${done ? "Accepted" : "Accepting"} the brief: ${action.title}`,
        ),
        accepts: done ? (appliedWhat(action.title) || action.title).toLowerCase() : undefined,
      };
    case "write_brief":
      return line(
        done ? "done" : "busy",
        "brief-write",
        done ? "Writing the brief" : "Writing the brief…",
      );
    case "recover_work_order":
    case "close_work_order":
    case "add_vision_item":
    case "start_autopilot":
    case "build_plan":
    case "start_planning": {
      // These say what they did in their result ("Re-checked “Sort”.").
      const [kind, fallback]: [QuietKind, string] =
        action.type === "close_work_order"
          ? ["work", "Closing the work"]
          : action.type === "add_vision_item"
            ? ["vision", `Adding to the vision: ${action.title}`]
            : action.type === "start_autopilot"
              ? ["autopilot", `Turning autopilot on for P${action.priority}`]
              : action.type === "build_plan"
                ? ["plan", "Building the plan"]
                : action.type === "start_planning"
                  ? ["plan", "Turning plan mode on"]
                  : ["work", "Picking the work back up"];
      return done
        ? line("done", kind, action.result || fallback)
        : line(action.status === "failed" ? "failed" : "busy", kind, `${fallback}…`);
    }
  }
  const what =
    appliedWhat(action.title) ||
    (action.type === "set_up_preview"
      ? "the app's setup"
      : action.type === "set_up_stack"
        ? "the stack"
        : action.type === "write_vision"
          ? "the vision"
          : "the work");
  const [verb, past] =
    action.type === "change_models"
      ? ["Switching models", "Switched models"]
      : action.type === "continue_work_order"
        ? ["Picking the work back up", "Picked the work back up"]
        : action.type === "set_up_preview"
          ? ["Fixing", "Fixing"]
          : action.type === "write_vision"
            ? ["Writing", "Writing"]
            : action.type === "set_up_stack"
              ? ["Setting up", "Setting up"]
              : action.hold_start || action.brief
                ? ["Creating", "Created"]
                : ["Starting", "Started"];
  const kind =
    action.type === "set_up_preview" ||
    action.type === "write_vision" ||
    action.type === "set_up_stack"
      ? "note"
      : workKind;
  if (action.status === "failed") return line("failed", kind, `Couldn't start: ${what}`);
  if (done) return line("done", kind, `${past}: ${what}`);
  if (action.queued) return line("info", kind, `Queued: ${what}, after ${action.after}`);
  return line("busy", kind, `${verb}: ${what}…`);
}

/** A turn's summary of work orders, as one quiet line counting them all. */
export function groupLine(piece: Extract<TurnPiece, { kind: "group" }>): QuietLine {
  const types = new Set(piece.actions.map((action) => action.type));
  const kind: QuietKind = types.has("create_work_order")
    ? "created"
    : [...types].every((type) => type === "start_work_order")
      ? "started"
      : "work";
  const tone: QuietTone = piece.items.some((item) => item.bucket === "failed")
    ? "failed"
    : piece.items.some((item) => item.bucket === "busy")
      ? "busy"
      : "done";
  return { tone, kind, count: piece.items.length, text: piece.summary.replace(/^✓ /, "") };
}

/** A line's text as compared for repeats: "Autopilot is on: finish P1 (6
 * items)." and "Autopilot is on: finish P1." are the same. */
export function repeatKey(text: string): string {
  return text
    .toLowerCase()
    .replace(/\s*\([^)]*\)/g, "")
    .replace(/[.…\s]+$/, "")
    .replace(/\s+/g, " ")
    .trim();
}

/** Drops the lines that repeat another: a brief's "is ready" note when its
 * acceptance follows, and a routine line saying again what one before it
 * said since the last message or card. */
export function dropRepeats<T>(entries: QuietEntry<T>[]): QuietEntry<T>[] {
  const accepted = new Set<string>();
  const kept: QuietEntry<T>[] = [];
  // Walk backwards so a note knows what follows it.
  for (let index = entries.length - 1; index >= 0; index--) {
    const entry = entries[index];
    if ("quiet" in entry) {
      if (entry.quiet.accepts) accepted.add(entry.quiet.accepts);
      if (entry.quiet.briefFor && accepted.has(entry.quiet.briefFor)) continue;
    }
    kept.push(entry);
  }
  kept.reverse();
  const out: QuietEntry<T>[] = [];
  let seen = new Set<string>();
  for (const entry of kept) {
    if (!("quiet" in entry)) {
      seen = new Set();
      out.push(entry);
      continue;
    }
    const key = repeatKey(entry.quiet.text);
    const routine = entry.quiet.tone === "done" || entry.quiet.tone === "info";
    if (routine && seen.has(key)) continue;
    seen.add(key);
    out.push(entry);
  }
  return out;
}

const plural = (n: number, one: string, many: string) => (n === 1 ? one : many);
const workOrders = (n: number) => `${n} work order${n === 1 ? "" : "s"}`;

/** One kind's part of a summary, for n lines of it. */
export function kindPhrase(kind: QuietKind, n: number): string {
  switch (kind) {
    case "brief":
      return plural(n, "accepted a brief", `accepted ${n} briefs`);
    case "brief-ready":
      return plural(n, "a brief is ready", `${n} briefs ready`);
    case "brief-write":
      return plural(n, "writing a brief", `writing ${n} briefs`);
    case "created":
      return `created ${workOrders(n)}`;
    case "started":
      return `started ${workOrders(n)}`;
    case "work":
      return `updated ${workOrders(n)}`;
    case "plan":
      return "updated the plan";
    case "research":
      return plural(n, "looked something up", `looked up ${n} things`);
    case "autopilot":
      return plural(n, "autopilot update", `${n} autopilot updates`);
    case "merged":
      return `${n} merged`;
    case "vision":
      return `added ${n} to the vision`;
    case "stack":
      return "set up the stack";
    case "answer":
      return plural(n, "answered a helper", `answered ${n} helpers`);
    case "order":
      return plural(n, "1 work order done", `${n} work orders done`);
    default:
      return plural(n, "1 update", `${n} updates`);
  }
}

/** At most this many kinds are named; the rest are counted. */
const SUMMARY_PARTS = 3;

/** A folded run's row, from the kinds in it: "Accepted 3 briefs · started 3
 * work orders". */
export function runSummary(lines: QuietLine[]): string {
  const tally = new Map<QuietKind, number>();
  for (const line of lines) tally.set(line.kind, (tally.get(line.kind) ?? 0) + line.count);
  const parts = [...tally].map(([kind, n]) => kindPhrase(kind, n));
  const shown = parts.slice(0, SUMMARY_PARTS);
  if (parts.length > SUMMARY_PARTS) shown.push(`${parts.length - SUMMARY_PARTS} more`);
  const text = shown.join(" · ");
  return text.charAt(0).toUpperCase() + text.slice(1);
}

/** The chat's rows with each run of routine lines between two other rows
 * folded into one summary; a run of one stays a line, and a line that stays
 * in view splits the run it is in. */
export function foldQuiet<T>(entries: QuietEntry<T>[]): QuietSegment<T>[] {
  const segments: QuietSegment<T>[] = [];
  let run: { line: QuietLine; node: T }[] = [];
  const flush = () => {
    if (run.length === 1) {
      segments.push({ kind: "line", ...run[0] });
    } else if (run.length > 1) {
      const lines = run.map((item) => item.line);
      segments.push({
        kind: "run",
        lines: run,
        summary: runSummary(lines),
        tone: lines.every((line) => line.tone === "done") ? "done" : "info",
      });
    }
    run = [];
  };
  for (const entry of entries) {
    if (!("quiet" in entry)) {
      flush();
      segments.push({ kind: "block", node: entry.block });
    } else if (staysInView(entry.quiet)) {
      flush();
      segments.push({ kind: "line", line: entry.quiet, node: entry.node });
    } else {
      run.push({ line: entry.quiet, node: entry.node });
    }
  }
  flush();
  return segments;
}

/** The glyph a line leads with. */
export const QUIET_GLYPH: Record<QuietTone, string> = {
  done: "✓",
  info: "•",
  busy: "•",
  needs: "!",
  failed: "✕",
};

/** A work order the chat started, as a quiet line: under way while an agent
 * works on it, needing the user when it waits for them, done once merged,
 * and otherwise just where it stands. */
export function orderLine(
  title: string,
  state: string,
  { running, waiting, done }: { running: boolean; waiting: boolean; done: boolean },
): QuietLine {
  const tone: QuietTone = waiting ? "needs" : running ? "busy" : done ? "done" : "info";
  return { tone, kind: "order", count: 1, text: state ? `${title} · ${state}` : title };
}
