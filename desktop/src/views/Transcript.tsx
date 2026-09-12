import { useState } from "react";
import type { ActivityDetail } from "../api/types";

// A phase transcript. Messages render as attributed bubbles; runs of low-signal
// "activity" events collapse into one expandable summary so the wall of "started
// a command / finished with exit code 0" becomes a single foldable row.
//
// Activity text is generic prose today ({id,type,text,at} is all the backend
// emits). When the coordinator adds structured command/file-change events, they
// slot into this same grouping — the summary can then read real command counts
// and file-diff stats instead of counting generic lines.

export interface TranscriptEntry {
  key: string;
  role: string; // "lead" | "reviewer" | "user" | agent id
  type: string; // "message" | "plan_submitted" | "activity" | ...
  text: string;
  activity?: ActivityDetail; // structured command / file-change detail
  at: string;
}

export function Transcript({
  entries,
  empty = "No activity yet.",
}: {
  entries: TranscriptEntry[];
  empty?: string;
}) {
  if (entries.length === 0) return <p className="muted">{empty}</p>;

  // Fold consecutive activity entries into groups; everything else passes through.
  const blocks: ({ kind: "entry"; e: TranscriptEntry } | { kind: "activity"; items: TranscriptEntry[] })[] = [];
  for (const e of entries) {
    if (e.type === "activity") {
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
  if (e.type === "plan_submitted") {
    return (
      <div className="plan-submitted">
        <span className="plan-submitted__label">📌 Plan submitted</span>
        <span className="msg__text">{e.text}</span>
      </div>
    );
  }
  const who = e.role === "reviewer" ? "Reviewer" : e.role === "user" ? "You" : "Lead";
  const cls = e.role === "reviewer" ? "msg--reviewer" : e.role === "user" ? "msg--user" : "msg--lead";
  return (
    <div className={`msg ${cls}`}>
      <span className="msg__who">{who}</span>
      <span className="msg__text">{e.text}</span>
    </div>
  );
}

function ActivityGroup({ items }: { items: TranscriptEntry[] }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="activity-group">
      <button className="activity-group__head" onClick={() => setOpen((o) => !o)}>
        <span className="activity-group__chevron">{open ? "▾" : "▸"}</span>
        {summarize(items)}
      </button>
      {open && (
        <div className="activity-group__items">
          {items.map((e) => (
            <ActivityItem key={e.key} e={e} />
          ))}
        </div>
      )}
    </div>
  );
}

function ActivityItem({ e }: { e: TranscriptEntry }) {
  const a = e.activity;
  if (a?.kind === "command") {
    return (
      <div className="activity-item">
        <span className="activity-item__cmd">
          <span className="activity-item__prompt">$</span> {a.command}
        </span>
        {a.exit_code !== undefined && (
          <span className={`chip chip--${a.exit_code === 0 ? "ok" : "bad"}`}>exit {a.exit_code}</span>
        )}
        {a.duration_ms !== undefined && <span className="activity-item__dur">{fmtMs(a.duration_ms)}</span>}
      </div>
    );
  }
  if (a?.kind === "file_change") {
    return (
      <div className="activity-item">
        <span className={`activity-item__op activity-item__op--${a.op}`}>{a.op}</span>
        <span className="activity-item__path">{a.op === "renamed" && a.old_path ? `${a.old_path} → ${a.path}` : a.path}</span>
        {(a.additions !== undefined || a.deletions !== undefined) && (
          <span className="activity-item__diff">
            {a.additions !== undefined && <span className="diff--add">+{a.additions}</span>}
            {a.deletions !== undefined && <span className="diff--del">−{a.deletions}</span>}
          </span>
        )}
      </div>
    );
  }
  return <div className="activity-item activity-item--note">{e.text}</div>;
}

function summarize(items: TranscriptEntry[]): string {
  const commands = items.filter((e) => e.activity?.kind === "command").length;
  const files = items.filter((e) => e.activity?.kind === "file_change").length;
  const role = items.every((e) => e.role === items[0].role) ? attributed(items[0].role) : null;
  const prefix = role ? `${role} · ` : "";
  const parts: string[] = [];
  if (commands) parts.push(`ran ${commands} command${commands === 1 ? "" : "s"}`);
  if (files) parts.push(`${files} file${files === 1 ? "" : "s"} changed`);
  if (parts.length === 0) {
    // Fall back to generic prose (pre-structured events).
    const cmds = items.filter((e) => /started a command/i.test(e.text)).length;
    if (cmds) parts.push(`ran ${cmds} command${cmds === 1 ? "" : "s"}`);
    else parts.push(`${items.length} step${items.length === 1 ? "" : "s"}`);
  }
  return prefix + parts.join(" · ");
}

function fmtMs(ms: number): string {
  return ms < 1000 ? `${ms}ms` : `${(ms / 1000).toFixed(1)}s`;
}

function attributed(role: string): string {
  if (role === "reviewer") return "Reviewer";
  if (role === "user") return "You";
  return "Lead";
}
