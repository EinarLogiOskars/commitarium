import type { RunConfig } from "../api/types";

// Form state for the stack's preview target. The port stays a string until
// saved so a half-typed value doesn't fight the input.
export type RunDraft = { service: string; port: string };

export function runDraftFrom(run?: RunConfig): RunDraft {
  return {
    service: run?.open.service ?? "",
    port: run?.open.port != null ? String(run.open.port) : "",
  };
}

/** The run config to save, or undefined when nothing is configured. */
export function runFromDraft(d: RunDraft): RunConfig | undefined {
  const service = d.service.trim();
  const port = d.port.trim();
  if (!service && !port) return undefined;
  return { open: { service, port: Number(port) } };
}

/** Explain the first problem with the preview target, or null when it is
 * either completely absent (allowed) or ready to save. */
export function runDraftIssue(d: RunDraft): string | null {
  const service = d.service.trim();
  const port = d.port.trim();
  if (!service && !port) return null;
  if (!service) return "Name the compose service to open.";
  if (!/^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$/.test(service)) {
    return "Use the service name exactly as it appears in the compose file.";
  }
  if (!port) return `Add the container port “${service}” listens on.`;
  if (!/^\d+$/.test(port) || Number(port) < 1 || Number(port) > 65535) {
    return "Use a port from 1 to 65535.";
  }
  return null;
}

export function runDraftComplete(d: RunDraft): boolean {
  return runDraftIssue(d) === null;
}

/** Choose what the preview opens in the browser (ADR-014). */
export function RunEditor({
  value,
  onChange,
  onAskAgent,
  disabled,
}: {
  value: RunDraft;
  onChange: (d: RunDraft) => void;
  onAskAgent?: () => void;
  disabled?: boolean;
}) {
  const configured = !!(value.service.trim() || value.port.trim());
  const issue = runDraftIssue(value);

  return (
    <div className="run">
      <div className="run__title">
        <div>
          <strong>Preview</strong>
          <p className="muted run__reason">
            Preview runs the compose file at the repository root (<code>compose.yaml</code>). Choose
            the service and container port to open in the browser.
          </p>
        </div>
        {configured && onAskAgent && (
          <button type="button" className="ghost" onClick={onAskAgent} disabled={disabled}>
            Ask an agent
          </button>
        )}
      </div>

      {!configured && onAskAgent && (
        <div className="run__empty">
          <div>
            <strong>Nothing to open yet</strong>
            <p className="muted">
              An agent can read the repository's compose file and tell you which service to open.
            </p>
          </div>
          <div className="run__empty-actions">
            <button type="button" className="primary" onClick={onAskAgent} disabled={disabled}>
              Set up with an agent
            </button>
          </div>
        </div>
      )}

      <div className="stack__row">
        <input
          type="text"
          className="mono"
          aria-label="Compose service"
          placeholder="service (e.g. web)"
          value={value.service}
          onChange={(e) => onChange({ ...value, service: e.target.value })}
          disabled={disabled}
        />
        <input
          type="text"
          inputMode="numeric"
          className="run__port"
          aria-label="Container port"
          placeholder="port"
          value={value.port}
          onChange={(e) => onChange({ ...value, port: e.target.value })}
          disabled={disabled}
        />
      </div>
      {issue && <div className="run__issue">{issue}</div>}
      <p className="muted note run__conventions">
        Servers listen on <code>0.0.0.0</code>, data lives in named volumes, and a frontend reaches
        its API through a dev proxy such as <code>/api</code>.
      </p>
    </div>
  );
}

/** Read-only view of a run config, e.g. in an assistant proposal. */
export function RunSummary({ run }: { run: RunConfig }) {
  return (
    <p className="run-summary">
      <span className="muted">Preview opens</span>{" "}
      <span className="mono">
        {run.open.service}:{run.open.port}
      </span>
    </p>
  );
}
