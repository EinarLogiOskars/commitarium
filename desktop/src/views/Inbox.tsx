import type { AttentionItem } from "../api/types";

/** Maps the coordinator's severity onto the shared state-dot tones. */
const TONE: Record<AttentionItem["severity"], "bad" | "warn" | "ok"> = {
  error: "bad",
  warning: "warn",
  info: "ok",
};

function when(iso: string): string {
  const seconds = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (seconds < 90) return "just now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}

// The cross-project review inbox. It renders the coordinator's snapshot as-is:
// ordering, severity and wording are backend decisions, so the same state never
// reads differently here than it does on a project dashboard.
export function Inbox({
  items,
  loaded,
  onOpen,
  onClose,
}: {
  items: AttentionItem[];
  loaded: boolean;
  onOpen: (projectId: string, featureId: string) => void;
  onClose: () => void;
}) {
  const actionable = items.filter((i) => i.actionable);
  const informational = items.filter((i) => !i.actionable);

  return (
    <div className="modal" onClick={onClose}>
      <div className="modal__card inbox" onClick={(e) => e.stopPropagation()}>
        <div className="panel__head">
          <h2>Inbox</h2>
          <button className="ghost" onClick={onClose}>
            Close
          </button>
        </div>

        {!loaded && <p className="muted">Loading…</p>}
        {loaded && items.length === 0 && (
          <p className="muted">Nothing needs you across any project.</p>
        )}

        {actionable.length > 0 && (
          <div className="inbox__group">
            {actionable.map((item) => (
              <Row key={item.id} item={item} onOpen={onOpen} />
            ))}
          </div>
        )}

        {informational.length > 0 && (
          <div className="inbox__group">
            <h3 className="inbox__heading">Recent activity</h3>
            {informational.map((item) => (
              <Row key={item.id} item={item} onOpen={onOpen} />
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

function Row({
  item,
  onOpen,
}: {
  item: AttentionItem;
  onOpen: (projectId: string, featureId: string) => void;
}) {
  const tone = TONE[item.severity];
  return (
    <button
      className={`inbox__item ${item.actionable ? "" : "inbox__item--muted"}`}
      onClick={() => onOpen(item.project_id, item.feature_id)}
    >
      <span className="inbox__top">
        <span className={`state state--${tone}`}>
          <span className={`dot dot--${tone}`} />
          {item.title}
        </span>
        <span className="inbox__when">{when(item.updated_at)}</span>
      </span>
      <span className="inbox__where">
        {item.project_name} · {item.feature_title}
      </span>
      {item.detail && <span className="inbox__detail">{item.detail}</span>}
    </button>
  );
}
