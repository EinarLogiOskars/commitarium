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
  disabled,
}: {
  value: RunDraft;
  onChange: (d: RunDraft) => void;
  disabled?: boolean;
}) {
  const issue = runDraftIssue(value);
  return (
    <div className="run">
      <div>
        <strong>Preview</strong>
        <p className="muted run__reason">
          Preview runs the project's <code>compose.yaml</code>, the same file you can run yourself
          with <code>docker compose up</code>. Choose the service and container port to open in the
          browser.
        </p>
      </div>
      <OpenTargetFields value={value} onChange={onChange} disabled={disabled} />
      {issue && <div className="run__issue">{issue}</div>}
    </div>
  );
}

/** Service and container port inputs, shared by the stack view and the Preview page. */
export function OpenTargetFields({
  value,
  onChange,
  disabled,
}: {
  value: RunDraft;
  onChange: (d: RunDraft) => void;
  disabled?: boolean;
}) {
  return (
    <div className="stack__row">
      <input
        type="text"
        className="mono"
        aria-label="Compose service"
        placeholder="service (e.g. frontend)"
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
