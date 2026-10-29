import { useState, type ReactNode } from "react";
import { QUIET_GLYPH, type QuietLine, type QuietTone } from "./quietLines";

// The chat's quiet lines (quietLines.ts): one look for Commitarium's notes
// and what Mayka set going, left under the messages with a glyph column.

/** A quiet line: its glyph, its text, and anything that goes with it (a
 * link, the lines under it). */
export function QuietRow({
  tone,
  title,
  pulse,
  children,
  below,
}: {
  tone: QuietTone;
  title?: string;
  /** Under way in the background: a pulse in place of the glyph. */
  pulse?: boolean;
  children: ReactNode;
  below?: ReactNode;
}) {
  return (
    <div className={`quiet quiet--${tone}`} title={title || undefined}>
      <p className="quiet__line">
        <span className="quiet__glyph" aria-hidden>
          {pulse ? <span className="assistant__pulse" /> : QUIET_GLYPH[tone]}
        </span>
        <span className="quiet__text">{children}</span>
      </p>
      {below}
    </div>
  );
}

/** A quiet line from its text alone, such as a Commitarium note. */
export function QuietText({ line }: { line: QuietLine }) {
  return <QuietRow tone={line.tone}>{line.text}</QuietRow>;
}

/** A run of routine lines folded into one row ("✓ Accepted 3 briefs ·
 * started 3 work orders ▸"); expanded, each line as it was. */
export function QuietRun({
  summary,
  tone,
  children,
}: {
  summary: string;
  tone: QuietTone;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className={`quiet quiet--${tone} quiet-run`}>
      <button
        className="quiet__line quiet__toggle"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        <span className="quiet__glyph" aria-hidden>
          {QUIET_GLYPH[tone]}
        </span>
        <span className="quiet__text">
          {summary} {open ? "▾" : "▸"}
        </span>
      </button>
      {open && <div className="quiet-run__lines">{children}</div>}
    </div>
  );
}
