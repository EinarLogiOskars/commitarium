import type { WorkingItem } from "./workingOn";

// The checklist under the chat while Mayka applies what the user asked for:
// a pulse for what is under way, a tick once done, a cross if it failed.
export function WorkingList({ items, writing }: { items: WorkingItem[]; writing: boolean }) {
  if (items.length === 0) return null;
  const doing = items.some((item) => item.state === "doing");
  return (
    <div className="working" role="status" aria-live="polite">
      <p className="working__head">
        {doing ? "Mayka is on it" : writing ? "Done. Mayka is writing…" : "Done"}
      </p>
      <ul className="working__items">
        {items.map((item) => (
          <li key={item.id} className={`working__item working__item--${item.state}`}>
            <span className="working__mark" aria-hidden>
              {item.state === "doing" ? (
                <span className="assistant__pulse" />
              ) : item.state === "done" ? (
                "✓"
              ) : (
                "✕"
              )}
            </span>
            {item.title}
          </li>
        ))}
      </ul>
    </div>
  );
}
