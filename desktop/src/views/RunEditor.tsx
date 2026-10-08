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

/** Every process row has a name, a command, and a numeric port if any. */
export function runDraftComplete(d: RunDraft): boolean {
  return d.processes.every(
    (p) => p.name.trim() && p.command.trim() && (!p.port.trim() || /^\d+$/.test(p.port.trim())),
  );
}

/** Edit how the app is installed and started for a preview (ADR-013). */
export function RunEditor({
  value,
  onChange,
  disabled,
}: {
  value: RunDraft;
  onChange: (d: RunDraft) => void;
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

  return (
    <div className="run">
      <label>Run (optional)</label>
      <p className="muted note">
        How to install and start the app so you can preview it. Processes should listen on 0.0.0.0,
        and a frontend should reach its API through a dev proxy (e.g. <code>/api</code>) rather than{" "}
        <code>localhost:&lt;port&gt;</code>.
      </p>

      <label className="run__label">
        Setup commands, one per line, run from the repository root
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
      {value.processes.length === 0 && (
        <p className="muted">No processes — the project can't be previewed until you add one.</p>
      )}
      {value.processes.map((p, i) => (
        <div className="stack__row run__process" key={i}>
          <input
            type="text"
            className="run__name"
            placeholder="name"
            value={p.name}
            onChange={(e) => setProcess(i, { name: e.target.value })}
            disabled={disabled}
          />
          <input
            type="text"
            className="mono"
            placeholder="command (e.g. npm run dev -- --host 0.0.0.0 --port 5173)"
            value={p.command}
            onChange={(e) => setProcess(i, { command: e.target.value })}
            disabled={disabled}
          />
          <input
            type="text"
            inputMode="numeric"
            className="run__port"
            placeholder="port"
            value={p.port}
            onChange={(e) => setProcess(i, { port: e.target.value })}
            disabled={disabled}
          />
          <label className="run__open" title="Open this one in the browser">
            <input type="radio" checked={p.open} onChange={() => markOpen(i)} disabled={disabled} />
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
      <div className="stack__actions">
        <button type="button" className="ghost" onClick={addProcess} disabled={disabled}>
          + Add process
        </button>
      </div>
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
