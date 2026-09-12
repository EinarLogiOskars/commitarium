import { useState } from "react";

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
            <div key={e.key} className="activity-group__item">
              {e.text}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function summarize(items: TranscriptEntry[]): string {
  const commands = items.filter((e) => /started a command/i.test(e.text)).length;
  const role = items.every((e) => e.role === items[0].role) ? attributed(items[0].role) : null;
  const prefix = role ? `${role} · ` : "";
  if (commands > 0) return `${prefix}ran ${commands} command${commands === 1 ? "" : "s"}`;
  return `${prefix}${items.length} step${items.length === 1 ? "" : "s"}`;
}

function attributed(role: string): string {
  if (role === "reviewer") return "Reviewer";
  if (role === "user") return "You";
  return "Lead";
}
