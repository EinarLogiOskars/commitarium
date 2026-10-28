import { useEffect, type ReactNode } from "react";

// The full detail of something shown compactly, over the page in the app's
// modal card. ✕, Escape, or a click outside the card dismisses it.
export function Overlay({
  title,
  onClose,
  children,
}: {
  title: string;
  onClose: () => void;
  children: ReactNode;
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div
      className="modal"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div className="modal__card overlay" role="dialog" aria-modal="true" aria-label={title}>
        <div className="overlay__head">
          <h2>{title}</h2>
          <button className="ghost overlay__close" onClick={onClose} aria-label="Close">
            ✕
          </button>
        </div>
        {children}
      </div>
    </div>
  );
}
