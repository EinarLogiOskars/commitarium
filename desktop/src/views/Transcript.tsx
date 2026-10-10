import { useEffect, useRef, useState } from "react";
import { Markdown } from "./Markdown";
import type { ActivityDetail } from "../api/types";

// A phase transcript. Messages render as attributed bubbles; runs of low-signal
// "activity" events collapse into one expandable summary so the wall of "started
// a command / finished with exit code 0" becomes a single foldable row.
//
// Activity text is generic prose today ({id,type,text,at} is all the backend
// emits). When the coordinator adds structured command/file-change events, they
// slot into this same grouping — the summary can then read real command counts
// and file-diff stats instead of counting generic lines.

// Agents emit their turns as a JSON envelope keyed by `action`. The human-
// readable prose lives in `content` (respond), `summary` (blocked, plan
// actions), or `message` (goal-clarification ask/propose); unwrap whichever is
// present, falling back to the raw string when it isn't a recognized envelope
// (plain text or an unknown shape).
function agentText(text: string): string {
  const t = text.trimStart();
  if (t[0] !== "{") return text;
  try {
    const v = JSON.parse(t);
    if (v && typeof v === "object") {
      if (typeof v.content === "string") return v.content;
      if (typeof v.summary === "string") return v.summary;
      if (typeof v.message === "string") return v.message;
    }
  } catch {
    /* not JSON — show as-is */
  }
  return text;
}

export interface TranscriptEntry {
  key: string;
  role: string; // "lead" | "reviewer" | "user" | agent id
  type: string; // "message" | "plan_submitted" | "activity" | ...
  text: string;
  activity?: ActivityDetail; // structured command / file-change detail
  at: string;
  streaming?: boolean; // a live message preview (not yet the durable final)
}

export function Transcript({
  entries,
  empty = "No activity yet.",
}: {
  entries: TranscriptEntry[];
  empty?: string;
}) {
  if (entries.length === 0) return <p className="muted">{empty}</p>;

  // Fold consecutive tool activity (commands/files) into collapsible groups.
  // Narration is presentation text — it stays inline like a message, so the
  // feed reads as the agent explaining itself between tool calls.
  const isTool = (e: TranscriptEntry) => e.type === "activity" && e.activity?.kind !== "narration";
  const blocks: (
    { kind: "entry"; e: TranscriptEntry } | { kind: "activity"; items: TranscriptEntry[] }
  )[] = [];
  for (const e of entries) {
    if (isTool(e)) {
      const last = blocks[blocks.length - 1];
      if (last && last.kind === "activity") last.items.push(e);
      else blocks.push({ kind: "activity", items: [e] });
    } else {
      blocks.push({ kind: "entry", e });
    }
  }

  return (
    <>
      {blocks.map((b, i) =>
        b.kind === "activity" ? (
          <ActivityGroup key={`act-${i}`} items={b.items} />
        ) : (
          <MessageEntry key={b.e.key} e={b.e} />
        ),
      )}
    </>
  );
}

function MessageEntry({ e }: { e: TranscriptEntry }) {
  if (e.type === "activity" && e.activity?.kind === "narration") {
    const who = e.role === "reviewer" ? "Reviewer" : "Lead";
    return (
      <div className={`narration ${e.role === "reviewer" ? "narration--reviewer" : ""}`}>
        <span className="narration__who">{who}</span>
        <div className="narration__text">
          <Markdown text={agentText(e.text)} />
        </div>
      </div>
    );
  }
  if (e.type === "plan_submitted") {
    return (
      <div className="plan-submitted">
        <span className="plan-submitted__label">📌 Plan submitted</span>
        <div className="msg__text">
          <Markdown text={agentText(e.text)} />
        </div>
      </div>
    );
  }
  const who =
    e.role === "reviewer"
      ? "Reviewer"
      : e.role === "user"
        ? "You"
        : e.role === "assistant"
          ? "Assistant"
          : "Lead";
  const cls =
    e.role === "reviewer" ? "msg--reviewer" : e.role === "user" ? "msg--user" : "msg--lead";
  return (
    <div className={`msg ${cls}${e.streaming ? " msg--streaming" : ""}`}>
      <span className="msg__who">{who}</span>
      <div className="msg__text">
        <Markdown text={agentText(e.text)} />
      </div>
    </div>
  );
}

function ActivityGroup({ items }: { items: TranscriptEntry[] }) {
  const [open, setOpen] = useState(false);
  const itemsRef = useRef<HTMLDivElement | null>(null);
  // Put a role's tool activity in the same lane as its chat bubbles.
  const sameRole = items.every((e) => e.role === items[0].role) ? items[0].role : null;

  // When expanded near the bottom of the scroll area, bring the revealed
  // content into view so it isn't hidden below the fold.
  useEffect(() => {
    if (open) itemsRef.current?.scrollIntoView({ block: "nearest" });
  }, [open]);

  const s = summarize(items);
  const hasDiff = s.additions > 0 || s.deletions > 0;
  return (
    <div className={`activity-group ${sameRole === "reviewer" ? "activity-group--reviewer" : ""}`}>
      <button
        className="activity-group__head"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
      >
        <span className="activity-group__summary">{s.text}</span>
        {hasDiff && <DiffStat additions={s.additions} deletions={s.deletions} />}
        <span className="activity-group__chevron">{open ? "⌄" : "›"}</span>
      </button>
      {open && (
        <div className="activity-group__items" ref={itemsRef}>
          {items.map((e) => (
            <ActivityItem key={e.key} e={e} />
          ))}
        </div>
      )}
    </div>
  );
}

function DiffStat({ additions, deletions }: { additions: number; deletions: number }) {
  return (
    <span className="diffstat">
      {additions > 0 && <span className="diff--add">+{additions}</span>}
      {deletions > 0 && <span className="diff--del">−{deletions}</span>}
    </span>
  );
}

// created → "Created", modified → "Updated", deleted → "Removed" (match the
// mental model of a change log rather than raw git op names).
const FILE_VERB: Record<string, string> = {
  created: "Created",
  modified: "Updated",
  deleted: "Removed",
  renamed: "Renamed",
};

function ActivityItem({ e }: { e: TranscriptEntry }) {
  const a = e.activity;
  if (a?.kind === "command") {
    const failed = a.exit_code !== undefined && a.exit_code !== 0;
    return (
      <div className="activity-item activity-item--cmd">
        <span className="activity-item__prompt">$</span>
        <span className="activity-item__cmd">{a.command}</span>
        <span className="activity-item__right">
          {a.exit_code !== undefined && (
            <span className={`chip chip--${failed ? "bad" : "ok"}`}>exit {a.exit_code}</span>
          )}
          {a.duration_ms !== undefined && (
            <span className="activity-item__dur">{fmtMs(a.duration_ms)}</span>
          )}
        </span>
      </div>
    );
  }
  if (a?.kind === "file_change") {
    const hasDiff = (a.additions ?? 0) > 0 || (a.deletions ?? 0) > 0;
    return (
      <div className="activity-item">
        <span className={`activity-item__verb activity-item__verb--${a.op}`}>
          {FILE_VERB[a.op] ?? a.op}
        </span>
        <span className="activity-item__path">
          {a.op === "renamed" && a.old_path ? `${a.old_path} → ${a.path}` : a.path}
        </span>
        {hasDiff && (
          <span className="activity-item__right">
            <DiffStat additions={a.additions ?? 0} deletions={a.deletions ?? 0} />
          </span>
        )}
      </div>
    );
  }
  return <div className="activity-item activity-item--note">{agentText(e.text)}</div>;
}

interface Summary {
  text: string;
  additions: number;
  deletions: number;
}

// A natural-language roll-up of a run of tool activity, plus the net diff across
// its file changes — e.g. "Ran 3 commands (1 failed), created 2 files, edited 1".
function summarize(items: TranscriptEntry[]): Summary {
  let commands = 0;
  let failed = 0;
  let created = 0;
  let edited = 0;
  let removed = 0;
  let renamed = 0;
  let additions = 0;
  let deletions = 0;
  for (const e of items) {
    const a = e.activity;
    if (a?.kind === "command") {
      commands++;
      if (a.exit_code !== undefined && a.exit_code !== 0) failed++;
    } else if (a?.kind === "file_change") {
      if (a.op === "created") created++;
      else if (a.op === "modified") edited++;
      else if (a.op === "deleted") removed++;
      else if (a.op === "renamed") renamed++;
      additions += a.additions ?? 0;
      deletions += a.deletions ?? 0;
    }
  }
  const parts: string[] = [];
  if (commands)
    parts.push(`ran ${commands} command${plural(commands)}${failed ? ` (${failed} failed)` : ""}`);
  if (created) parts.push(`created ${created} file${plural(created)}`);
  if (edited) parts.push(`edited ${edited} file${plural(edited)}`);
  if (removed) parts.push(`removed ${removed} file${plural(removed)}`);
  if (renamed) parts.push(`renamed ${renamed} file${plural(renamed)}`);
  if (parts.length === 0) {
    // Pre-structured fallback (older coordinators emit generic prose).
    const cmds = items.filter((e) => /started a command/i.test(e.text)).length;
    parts.push(
      cmds ? `ran ${cmds} command${plural(cmds)}` : `${items.length} step${plural(items.length)}`,
    );
  }
  const joined = parts.join(", ");
  return { text: joined.charAt(0).toUpperCase() + joined.slice(1), additions, deletions };
}

function plural(n: number): string {
  return n === 1 ? "" : "s";
}

function fmtMs(ms: number): string {
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`;
}
