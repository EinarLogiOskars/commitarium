import type { ServiceEvidence } from "../api/types";
import { failingFirst, isFailing, serviceState } from "./previewEvidence";

/** What each container did, failing ones first with their last log lines
 * open: why a container stopped is in its own log, not the build output. */
export function ContainerEvidence({ services }: { services: ServiceEvidence[] }) {
  return (
    <ul className="evidence">
      {failingFirst(services).map((s) => (
        <li
          key={s.service}
          className={`evidence__service ${isFailing(s) ? "evidence__service--bad" : ""}`}
        >
          <details open={isFailing(s)}>
            <summary>
              <strong className="mono">{s.service}</strong>{" "}
              <span className="muted">{serviceState(s)}</span>
            </summary>
            <pre className="preview__logs mono">{s.logs?.trimEnd() || "No output."}</pre>
          </details>
        </li>
      ))}
    </ul>
  );
}
