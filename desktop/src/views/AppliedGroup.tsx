import { useState } from "react";
import type { WorkItem } from "./appliedSummary";
import { QUIET_GLYPH, type QuietTone } from "./quietLines";

// A turn's work actions as one summary line, collapsed by default; expanded,
// a line per work order whose title opens it.
export function AppliedGroup({
  summary,
  items,
  tone,
  onOpenOrder,
}: {
  summary: string;
  items: WorkItem[];
  tone: QuietTone;
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
        <span className="quiet__glyph" aria-hidden>
          {QUIET_GLYPH[tone]}
        </span>
        <span className="quiet__text">
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
