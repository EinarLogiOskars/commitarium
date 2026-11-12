import { REAL_APP_LABEL, realAppOffer, useRealApp } from "./realApp";

/** The ways to open a project's real app, the one used last first, or why
 * there is none yet (HELPERS_PLAN.md H5). The preview bar and the Apps page
 * cards show the same. */
export function RealAppButtons({
  projectId,
  name,
  isApp,
  className,
}: {
  projectId: string;
  name: string;
  /** Whether the project is on the Apps page, which on this computer makes
   * it installed. */
  isApp: boolean;
  className?: string;
}) {
  const { real, busy, error, open } = useRealApp(projectId, name);
  if (!real) return null;
  const offer = realAppOffer(real, isApp);
  return (
    <span className={`real-app ${className ?? ""}`}>
      {offer.kind === "missing" ? (
        <span className="real-app__missing">{offer.text}</span>
      ) : (
        offer.buttons.map((how) => (
          <button
            key={how}
            className="real-app__button"
            disabled={busy !== null}
            onClick={(e) => {
              // On an Apps page tile, which opens the window itself.
              e.stopPropagation();
              void open(how);
            }}
            onKeyDown={(e) => e.stopPropagation()}
          >
            {busy === how && how === "browser" ? "Opening…" : REAL_APP_LABEL[how]}
          </button>
        ))
      )}
      {error && <span className="real-app__error">{error}</span>}
    </span>
  );
}
