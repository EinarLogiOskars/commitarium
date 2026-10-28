import { useState } from "react";
import type { WorkItem } from "./appliedSummary";

// A turn's work actions as one summary line, collapsed by default; expanded,
// a line per work order whose title opens it.
export function AppliedGroup({
  summary,
  items,
  failed,
  onOpenOrder,
}: {
  summary: string;
  items: WorkItem[];
  failed: boolean;
  onOpenOrder?: (featureId: string) => void;
}) {
  const [open, setOpen] = useState(false);
  return (
    <div className="assistant__applied-wrap assistant__applied-group">
      <button
        className={`linkish muted assistant__applied ${failed ? "assistant__applied--failed" : ""}`}
        aria-expanded={open}
        onClick={() => setOpen(!open)}
      >
        {summary} {open ? "▾" : "▸"}
      </button>
      {open && (
        <ul className="assistant__applied-items">
          {items.map((item) => (
            <li
              key={item.key}
              className={`muted ${item.bucket === "failed" ? "assistant__applied--failed" : ""}`}
            >
              {item.workOrderId && onOpenOrder ? (
                <button className="linkish" onClick={() => onOpenOrder(item.workOrderId!)}>
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
