import { openExternal } from "../ipc";
import { PREVIEW_STATE, primaryUrl, type PreviewHandle } from "./usePreview";

/** Preview entry in the project rail, on every project page. The label opens
 * the Preview page; the quick action starts the preview and opens it in the
 * browser, or reopens it while running. */
export function PreviewRailItem({
  preview,
  runnable,
  active,
  onOpenPage,
}: {
  preview: PreviewHandle;
  /** The stack has run commands, so a preview can start. */
  runnable: boolean;
  active: boolean;
  onOpenPage: () => void;
}) {
  const state = preview.status?.state;
  const shown = state && state !== "stopped" ? PREVIEW_STATE[state] : null;
  const url = state === "running" ? primaryUrl(preview.status) : null;

  return (
    <div className="rail__preview">
      <button className={`rail__item ${active ? "rail__item--active" : ""}`} onClick={onOpenPage}>
        <span className="rail__item-title">Preview</span>
        {shown && (
          <span
            className={`dot dot--${shown.tone} ${shown.pulse ? "dot--pulse" : ""}`}
            title={shown.label}
          />
        )}
      </button>
      {url ? (
        <button className="rail__new" title="Open preview" onClick={() => void openExternal(url)}>
          ↗
        </button>
      ) : (
        runnable &&
        state !== "starting" && (
          <button className="rail__new" title="Run preview" onClick={() => void preview.start()}>
            ▶
          </button>
        )
      )}
    </div>
  );
}
