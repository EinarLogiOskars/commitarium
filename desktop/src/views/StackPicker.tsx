import { useEffect, useMemo, useState } from "react";
import {
  detectProjectToolchain,
  getProjectToolchain,
  getToolchainPresets,
  updateProjectToolchain,
} from "../api/toolchains";
import { ApiError } from "../api/client";
import { TOOL_NAMES } from "../api/types";
import { SetupAssistant } from "./SetupAssistant";
import type {
  AgentProvider,
  ProjectToolchain,
  ProvisioningStatus,
  ToolchainPreset,
  ToolchainSource,
  ToolName,
} from "../api/types";

type Row = { tool: string; version: string };

function rowsFrom(tools: Record<string, string>): Row[] {
  return Object.entries(tools)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([tool, version]) => ({ tool, version }));
}

/** Choose the runtime toolchain for a project. Presets give exact versions in
 * one click; custom rows and repo detection cover the rest. Saving is what makes
 * a project's toolchain "configured" — the gate for creating work orders. */
export function StackPicker({
  projectId,
  preferred,
  onSaved,
}: {
  projectId: string;
  preferred?: { provider?: AgentProvider; model?: string };
  onSaved: (t: ProjectToolchain) => void;
}) {
  const [presets, setPresets] = useState<ToolchainPreset[]>([]);
  const [current, setCurrent] = useState<ProjectToolchain | null>(null);
  const [rows, setRows] = useState<Row[]>([]);
  const [services, setServices] = useState<string[]>([]);
  const [source, setSource] = useState<ToolchainSource>("picker");
  const [serviceDraft, setServiceDraft] = useState("");
  const [detection, setDetection] = useState<{ evidence: string[]; confidence: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [detecting, setDetecting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [helping, setHelping] = useState(false);

  useEffect(() => {
    let live = true;
    void (async () => {
      try {
        const [{ presets }, tc] = await Promise.all([
          getToolchainPresets(),
          getProjectToolchain(projectId),
        ]);
        if (!live) return;
        setPresets(presets);
        setCurrent(tc);
        if (tc.status === "configured") {
          setRows(rowsFrom(tc.tools));
          setServices(tc.services);
          setSource(tc.source ?? "picker");
        }
      } catch (e) {
        if (live) setError(describe(e));
      }
    })();
    return () => {
      live = false;
    };
  }, [projectId]);

  // Follow runtime installation to completion once a stack is configured.
  const provisioning = current?.provisioning_status;
  useEffect(() => {
    if (provisioning !== "pending" && provisioning !== "installing") return;
    const t = setInterval(async () => {
      try {
        setCurrent(await getProjectToolchain(projectId));
      } catch {
        /* transient — keep the last known state */
      }
    }, 2500);
    return () => clearInterval(t);
  }, [provisioning, projectId]);

  const applyPreset = (p: ToolchainPreset) => {
    setRows(rowsFrom(p.tools));
    setServices(p.services ?? []);
    setSource("picker");
    setDetection(null);
    setError(null);
  };

  const setRow = (i: number, patch: Partial<Row>) => {
    setRows((rs) => rs.map((r, idx) => (idx === i ? { ...r, ...patch } : r)));
    setSource("picker");
  };
  const addRow = () => setRows((rs) => [...rs, { tool: "", version: "" }]);
  const removeRow = (i: number) => setRows((rs) => rs.filter((_, idx) => idx !== i));

  const addService = () => {
    const s = serviceDraft.trim().toLowerCase();
    if (s && !services.includes(s)) setServices((v) => [...v, s]);
    setServiceDraft("");
  };
  const removeService = (s: string) => setServices((v) => v.filter((x) => x !== s));

  const detect = async () => {
    setDetecting(true);
    setError(null);
    try {
      const s = await detectProjectToolchain(projectId);
      setRows(rowsFrom(s.tools));
      setServices(s.services);
      setSource("detected");
      setDetection({ evidence: s.evidence, confidence: s.confidence });
    } catch (e) {
      setError(describe(e));
    } finally {
      setDetecting(false);
    }
  };

  // Collapse rows to an exact tool->version map; last entry wins on duplicates.
  const tools = useMemo(() => {
    const out: Record<string, string> = {};
    for (const r of rows) {
      const tool = r.tool.trim();
      const version = r.version.trim();
      if (tool && version) out[tool] = version;
    }
    return out;
  }, [rows]);

  const complete = rows.length > 0 && rows.every((r) => r.tool && r.version.trim());
  const canSave = complete && Object.keys(tools).length > 0 && !busy;

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      onSaved(await updateProjectToolchain(projectId, { source, tools, services }));
    } catch (e) {
      setError(describe(e));
    } finally {
      setBusy(false);
    }
  };

  if (helping) {
    return (
      <section className="panel">
        <h2>Stack — ask an agent</h2>
        <SetupAssistant
          projectId={projectId}
          preferred={preferred}
          onApplied={(t) => {
            setHelping(false);
            onSaved(t);
          }}
          onCancel={() => setHelping(false)}
        />
      </section>
    );
  }

  return (
    <section className="panel">
      <div className="panel__head">
        <h2>Stack</h2>
        <button className="ghost" onClick={() => setHelping(true)} disabled={busy}>
          Ask an agent
        </button>
      </div>
      {current?.status === "needs_setup" && (
        <p className="muted">
          Choose the runtime this project's agents build with. You can create work orders once a
          stack is saved.
        </p>
      )}
      {current?.status === "configured" && provisioning && (
        <ProvisioningBanner status={provisioning} message={current.provisioning_message} />
      )}
      {error && <div className="banner banner--error">{error}</div>}

      <div className="stack__presets">
        {presets.map((p) => (
          <button key={p.id} type="button" className="stack__preset" onClick={() => applyPreset(p)}>
            <span className="stack__preset-name">{p.display_name}</span>
            <span className="muted stack__preset-desc">{p.description}</span>
            <span className="stack__preset-tools">
              {Object.entries(p.tools)
                .map(([t, v]) => `${t} ${v}`)
                .join(" · ")}
            </span>
          </button>
        ))}
      </div>

      <div className="stack__tools">
        {rows.length === 0 && <p className="muted">No tools yet — pick a preset above or add one.</p>}
        {rows.map((r, i) => (
          <div className="stack__row" key={i}>
            <select
              value={r.tool}
              onChange={(e) => setRow(i, { tool: e.target.value as ToolName })}
              disabled={busy}
            >
              <option value="">Tool…</option>
              {TOOL_NAMES.map((t) => (
                <option key={t} value={t}>
                  {t}
                </option>
              ))}
            </select>
            <input
              type="text"
              placeholder="exact version (e.g. 3.14.7)"
              value={r.version}
              onChange={(e) => setRow(i, { version: e.target.value })}
              disabled={busy}
            />
            <button type="button" className="ghost" onClick={() => removeRow(i)} disabled={busy}>
              Remove
            </button>
          </div>
        ))}
        <div className="stack__actions">
          <button type="button" className="ghost" onClick={addRow} disabled={busy}>
            + Add tool
          </button>
          <button type="button" className="ghost" onClick={() => void detect()} disabled={busy || detecting}>
            {detecting ? "Detecting…" : "Detect from repository"}
          </button>
        </div>
        {detection && (
          <p className="muted note">
            Detected ({detection.confidence} confidence)
            {detection.evidence.length > 0 && <> from {detection.evidence.join(", ")}</>}. Review, then
            save.
          </p>
        )}
      </div>

      <div className="stack__services">
        <label>External services (optional)</label>
        <p className="muted note">
          Recorded as requirements only — Commitarium does not start databases or other servers in
          this release.
        </p>
        <div className="stack__chips">
          {services.map((s) => (
            <span key={s} className="pill">
              {s}
              <button type="button" className="pill__x" onClick={() => removeService(s)} disabled={busy}>
                ×
              </button>
            </span>
          ))}
        </div>
        <div className="stack__row">
          <input
            type="text"
            placeholder="e.g. postgresql"
            value={serviceDraft}
            onChange={(e) => setServiceDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                addService();
              }
            }}
            disabled={busy}
          />
          <button type="button" className="ghost" onClick={addService} disabled={busy || !serviceDraft.trim()}>
            Add
          </button>
        </div>
      </div>

      <button className="primary" onClick={() => void save()} disabled={!canSave}>
        {busy ? "Saving…" : current?.status === "configured" ? "Save changes" : "Save stack"}
      </button>
    </section>
  );
}

function ProvisioningBanner({
  status,
  message,
}: {
  status: ProvisioningStatus;
  message?: string;
}) {
  const cls =
    status === "ready" ? "banner--ok" : status === "failed" ? "banner--error" : "banner--info";
  const label: Record<ProvisioningStatus, string> = {
    pending: "Runtime install queued",
    installing: "Installing runtimes…",
    ready: "Runtimes ready",
    failed: "Runtime install failed",
  };
  return (
    <div className={`banner ${cls}`}>
      <strong>{label[status]}</strong>
      {message ? ` — ${message}` : ""}
    </div>
  );
}

function describe(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return String(e);
}
