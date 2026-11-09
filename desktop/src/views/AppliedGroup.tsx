import { useState } from "react";
import type { WorkItem } from "./appliedSummary";
import { QUIET_GLYPH, type QuietTone } from "./quietLines";

// A turn's work actions, or its helpers at work, as one summary line,
// collapsed by default; expanded, a line per work order whose title opens it.
// While any of them works, the line pulses.
export function AppliedGroup({
  summary,
  items,
  tone,
  pulse,
  onOpenOrder,
}: {
  summary: string;
  items: WorkItem[];
  tone: QuietTone;
  pulse?: boolean;
  onOpenOrder?: (featureId: string) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className={`quiet quiet--${tone}`}>
      <button
        className="quiet__line quiet__toggle"
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        {(pulse || QUIET_GLYPH[tone]) && (
          <span className="quiet__glyph" aria-hidden>
            {pulse ? <span className="assistant__pulse" /> : QUIET_GLYPH[tone]}
          </span>
        )}
        <span className={pulse ? "quiet__text live-text" : "quiet__text"}>
          {summary.replace(/^✓ /, "")} {open ? "▾" : "▸"}
        </span>
      </button>
      {open && (
        <ul className="quiet__items">
          {items.map((item) => (
            <li key={item.key} className={item.bucket === "failed" ? "quiet--failed" : undefined}>
              {item.workOrderId && onOpenOrder ? (
                <button className="quiet__link" onClick={() => onOpenOrder(item.workOrderId!)}>
                  {item.title}
                </button>
              ) : (
                item.title
              )}
              : {item.state}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
