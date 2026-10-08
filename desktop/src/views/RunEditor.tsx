import type { RunConfig } from "../api/types";

// Form state for the stack's run config. Setup is one command per line; ports
// stay strings until saved so a half-typed value doesn't fight the input.
export type ProcessDraft = { name: string; command: string; port: string; open: boolean };
export type RunDraft = { setup: string; processes: ProcessDraft[] };

export function runDraftFrom(run?: RunConfig): RunDraft {
  return {
    setup: (run?.setup ?? []).join("\n"),
    processes: (run?.processes ?? []).map((p) => ({
      name: p.name,
      command: p.command,
      port: p.port != null ? String(p.port) : "",
      open: !!p.open,
    })),
  };
}

/** The run config to save, or undefined when nothing is configured. */
export function runFromDraft(d: RunDraft): RunConfig | undefined {
  const setup = d.setup
    .split("\n")
    .map((l) => l.trim())
    .filter(Boolean);
  if (d.processes.length === 0 && setup.length === 0) return undefined;
  return {
    setup,
    processes: d.processes.map((p) => ({
      name: p.name.trim(),
      command: p.command.trim(),
      ...(p.port.trim() ? { port: Number(p.port) } : {}),
      ...(p.open ? { open: true } : {}),
    })),
  };
}

/** Explain the first problem with a manual preview setup, or null when it is
 * either completely absent (allowed) or ready to save. */
export function runDraftIssue(d: RunDraft): string | null {
  const hasSetup = d.setup.split("\n").some((line) => line.trim());
  if (!hasSetup && d.processes.length === 0) return null;
  if (d.processes.length === 0) return "Add at least one process to make the project previewable.";

  const names = new Set<string>();
  const ports = new Set<number>();
  let open = 0;
  for (const process of d.processes) {
    const name = process.name.trim();
    const command = process.command.trim();
    if (!/^[a-z][a-z0-9-]{0,31}$/.test(name)) {
      return "Process names start with a lowercase letter and use only lowercase letters, numbers, and hyphens.";
    }
    if (names.has(name)) return `Process name “${name}” is used more than once.`;
    names.add(name);
    if (!command) return `Add a command for “${name}”.`;
    if (process.port.trim()) {
      if (!/^\d+$/.test(process.port.trim())) return `Use a numeric port for “${name}”.`;
      const port = Number(process.port);
      if (port < 1 || port > 65535) return `Use a port from 1 to 65535 for “${name}”.`;
      if (ports.has(port)) return `Port ${port} is used more than once.`;
      ports.add(port);
    }
    if (process.open) {
      open += 1;
      if (!process.port.trim()) return `Add a port for “${name}” so it can open in the browser.`;
    }
  }
  if (open > 1) return "Choose only one process to open in the browser.";
  return null;
}

/** Every configured process is complete and the whole run contract is valid. */
export function runDraftComplete(d: RunDraft): boolean {
  return runDraftIssue(d) === null;
}

/** Edit how the app is installed and started for a preview (ADR-013). */
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
  const setProcess = (i: number, patch: Partial<ProcessDraft>) =>
    onChange({
      ...value,
      processes: value.processes.map((p, idx) => (idx === i ? { ...p, ...patch } : p)),
    });
  const markOpen = (i: number) =>
    onChange({ ...value, processes: value.processes.map((p, idx) => ({ ...p, open: idx === i })) });
  const addProcess = () =>
    onChange({
      ...value,
      processes: [...value.processes, { name: "", command: "", port: "", open: false }],
    });
  const removeProcess = (i: number) =>
    onChange({ ...value, processes: value.processes.filter((_, idx) => idx !== i) });
  const configured = value.processes.length > 0 || value.setup.split("\n").some((l) => l.trim());
  const issue = runDraftIssue(value);

  return (
    <div className="run">
      <div className="run__title">
        <div>
          <strong>Preview setup</strong>
          <p className="muted run__reason">
            Tell Commitarium how to install and start this project for Preview.
          </p>
        </div>
        {configured && onAskAgent && (
          <button type="button" className="ghost" onClick={onAskAgent} disabled={disabled}>
            Improve with an agent
          </button>
        )}
      </div>

      {!configured ? (
        <div className="run__empty">
          <div>
            <strong>No preview setup yet</strong>
            <p className="muted">
              An agent can inspect the committed repository and propose the setup commands,
              processes, and ports for you.
            </p>
          </div>
          <div className="run__empty-actions">
            {onAskAgent && (
              <button type="button" className="primary" onClick={onAskAgent} disabled={disabled}>
                Set up with an agent
              </button>
            )}
            <button type="button" className="ghost" onClick={addProcess} disabled={disabled}>
              Enter commands manually
            </button>
          </div>
        </div>
      ) : (
        <>
          <p className="muted note run__conventions">
            Processes listen on <code>0.0.0.0</code>. Frontends reach APIs through a dev proxy such
            as <code>/api</code>, not a hardcoded <code>localhost:&lt;port&gt;</code>.
          </p>

          <label className="run__label">
            Setup commands <span className="muted">(optional, one per line)</span>
          </label>
          <textarea
            className="mono"
            rows={3}
            placeholder={"cd backend && uv sync\ncd frontend && npm ci"}
            value={value.setup}
            onChange={(e) => onChange({ ...value, setup: e.target.value })}
            disabled={disabled}
          />

          <label className="run__label">Processes</label>
          {value.processes.map((p, i) => (
            <div className="stack__row run__process" key={i}>
              <input
                type="text"
                className="run__name"
                aria-label={`Process ${i + 1} name`}
                placeholder="name"
                value={p.name}
                onChange={(e) => setProcess(i, { name: e.target.value })}
                disabled={disabled}
              />
              <input
                type="text"
                className="mono"
                aria-label={`Process ${i + 1} command`}
                placeholder="command (e.g. npm run dev -- --host 0.0.0.0 --port 5173)"
                value={p.command}
                onChange={(e) => setProcess(i, { command: e.target.value })}
                disabled={disabled}
              />
              <input
                type="text"
                inputMode="numeric"
                className="run__port"
                aria-label={`Process ${i + 1} port`}
                placeholder="port"
                value={p.port}
                onChange={(e) => setProcess(i, { port: e.target.value })}
                disabled={disabled}
              />
              <label className="run__open" title="Open this one in the browser">
                <input
                  type="radio"
                  name="preview-open-process"
                  checked={p.open}
                  onChange={() => markOpen(i)}
                  disabled={disabled}
                />
                Open
              </label>
              <button
                type="button"
                className="ghost"
                onClick={() => removeProcess(i)}
                disabled={disabled}
              >
                Remove
              </button>
            </div>
          ))}
          {issue && <div className="run__issue">{issue}</div>}
          <div className="stack__actions">
            <button type="button" className="ghost" onClick={addProcess} disabled={disabled}>
              + Add process
            </button>
          </div>
        </>
      )}
    </div>
  );
}

/** Read-only view of a run config, e.g. in an assistant proposal. */
export function RunSummary({ run }: { run: RunConfig }) {
  return (
    <div className="run-summary">
      {run.setup.length > 0 && (
        <>
          <span className="muted">Setup</span>
          <ul className="mono">
            {run.setup.map((c, i) => (
              <li key={i}>{c}</li>
            ))}
          </ul>
        </>
      )}
      <span className="muted">Processes</span>
      <ul className="mono">
        {run.processes.map((p) => (
          <li key={p.name}>
            <strong>{p.name}</strong>
            {p.port != null && ` :${p.port}`}
            {p.open && " (opens)"} — {p.command}
          </li>
        ))}
      </ul>
    </div>
  );
}
