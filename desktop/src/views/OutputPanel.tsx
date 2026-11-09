import { useCallback, useEffect, useState } from "react";
import {
  OUTPUTS_CHANGED,
  type HelperFile,
  type OutputFile,
  type OutputVersion,
  listHelperFiles,
  listOutputVersions,
  listOutputs,
  markOutputDone,
  readHelperFile,
  readOutput,
  restoreOutputVersion,
} from "../api/outputs";
import { OutputGallery, OutputView } from "./Outputs";
import {
  type PanelTarget,
  imagesLabel,
  liveFor,
  liveHelper,
  panelTitle,
  shownKey,
  storedFor,
  versionLabel,
} from "./panelTargets";

// The file beside the chat (HELPERS_PLAN.md, H8): an output the user opened,
// or a helper's file while it is built. It reads the file again whenever it
// changes, steps back through its versions and restores one, and marks it
// Done, which lets its helper go.

const POLL_MS = 4_000;

const describe = (e: unknown) => (e instanceof Error ? e.message : String(e));

export function OutputPanel({
  owner,
  target,
  atWork,
  onClose,
}: {
  owner: string;
  target: PanelTarget;
  /** The helpers this chat briefed that are at work now. */
  atWork: ReadonlySet<string>;
  onClose: () => void;
}) {
  const [files, setFiles] = useState<OutputFile[]>([]);
  // The helper's folder as last looked at, by helper.
  const [looked, setLive] = useState<{ helper: string; files: HelperFile[] } | null>(null);
  // An older version being looked at; null is the latest.
  const [version, setVersion] = useState<number | null>(null);
  const [listedVersions, setVersions] = useState<{
    name: string;
    versions: OutputVersion[];
  } | null>(null);
  const [collapsed, setCollapsed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const loadFiles = useCallback(() => {
    listOutputs(owner)
      .then((r) => setFiles(r.outputs ?? []))
      .catch(() => undefined);
  }, [owner]);
  useEffect(() => {
    loadFiles();
    const timer = setInterval(loadFiles, POLL_MS);
    window.addEventListener(OUTPUTS_CHANGED, loadFiles);
    return () => {
      clearInterval(timer);
      window.removeEventListener(OUTPUTS_CHANGED, loadFiles);
    };
  }, [loadFiles]);

  const file = storedFor(target, files);
  const helper = target.images ? null : liveHelper(target, file, atWork);
  // While its helper works on it, the file shows as it is built.
  useEffect(() => {
    if (!helper) return;
    let gone = false;
    const look = () =>
      listHelperFiles(owner, helper)
        .then((r) => !gone && setLive({ helper, files: r.files ?? [] }))
        .catch(() => !gone && setLive({ helper, files: [] }));
    look();
    const timer = setInterval(look, POLL_MS);
    return () => {
      gone = true;
      clearInterval(timer);
    };
  }, [owner, helper]);
  const live = helper && looked?.helper === helper ? looked.files : [];
  const liveFile = helper && version === null ? liveFor(target, file, live) : null;

  const latest = file?.version ?? 1;
  const fileName = file?.name;
  useEffect(() => {
    if (!fileName) return;
    let gone = false;
    listOutputVersions(owner, fileName)
      .then((r) => !gone && setVersions({ name: fileName, versions: r.versions ?? [] }))
      .catch(() => !gone && setVersions({ name: fileName, versions: [] }));
    return () => {
      gone = true;
    };
  }, [owner, fileName, latest]);

  const versions = fileName && listedVersions?.name === fileName ? listedVersions.versions : [];
  const act = (work: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    work()
      .catch((e) => setError(describe(e)))
      .finally(() => setBusy(false));
  };
  const restore = (number: number) =>
    act(async () => {
      if (!file) return;
      await restoreOutputVersion(owner, file.name, number);
      setVersion(null);
      window.dispatchEvent(new Event(OUTPUTS_CHANGED));
    });
  const done = () =>
    act(async () => {
      if (!file) return;
      await markOutputDone(owner, file.name);
      window.dispatchEvent(new Event(OUTPUTS_CHANGED));
    });

  const title = target.images ? imagesLabel(target.images) : panelTitle(target, file);
  const shownName = liveFile?.name ?? file?.name ?? target.live ?? target.name ?? "";
  const shown = shownKey(file, version, liveFile);
  const load = useCallback(() => {
    if (liveFile && helper) return readHelperFile(owner, helper, liveFile.name);
    return readOutput(owner, file?.name ?? shownName, version ?? undefined);
    // shown names what to read.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [owner, shown]);

  if (collapsed) {
    return (
      <aside className="output-panel output-panel--collapsed" aria-label={title}>
        <button
          className="ghost output-panel__unfold"
          onClick={() => setCollapsed(false)}
          title={`Show ${title}`}
        >
          ‹ <span className="output-panel__unfold-name">{title}</span>
        </button>
      </aside>
    );
  }

  const canShow = target.images || liveFile || file;
  return (
    <aside className="output-panel" aria-label={title}>
      <header className="output-panel__head">
        <h2 className="output-panel__title" title={title}>
          {title}
        </h2>
        {helper && (
          <span className="pill output-panel__live" title="It updates as the helper works">
            <span className="activity-dot activity-dot--running" aria-hidden />
            {file ? "Changing" : "Being made"}
          </span>
        )}
        <span className="chat-view__spacer" />
        <button
          className="ghost"
          onClick={() => setCollapsed(true)}
          aria-label="Hide the file"
          title="Hide"
        >
          ›
        </button>
        <button className="ghost" onClick={onClose} aria-label="Close the file" title="Close">
          ✕
        </button>
      </header>
      {!target.images && file && (versions.length > 1 || file.kept || file.working) && (
        <div className="output-panel__versions">
          {versions.length > 1 && (
            <label className="output-panel__version">
              <span className="muted">Version</span>
              <select
                value={version ?? latest}
                onChange={(e) => {
                  const number = Number(e.target.value);
                  setVersion(number === latest ? null : number);
                }}
              >
                {versions.map((v) => (
                  <option key={v.number} value={v.number}>
                    {versionLabel(v, latest)}
                  </option>
                ))}
              </select>
            </label>
          )}
          {version !== null && (
            <>
              <button onClick={() => restore(version)} disabled={busy}>
                Restore v{version}
              </button>
              <button className="ghost" onClick={() => setVersion(null)}>
                Latest
              </button>
            </>
          )}
          <span className="chat-view__spacer" />
          {(file.kept || file.working) && (
            <button
              onClick={done}
              disabled={busy}
              title="Finished with it: the helper lets go of its work; the versions stay"
            >
              Done
            </button>
          )}
          {error && <span className="chat-view__error">{error}</span>}
        </div>
      )}
      <div className="output-panel__body">
        {target.images ? (
          <OutputGallery owner={owner} images={target.images} />
        ) : canShow ? (
          <OutputView
            key={shownName}
            owner={owner}
            name={shownName}
            load={load}
            shown={shown}
            file={file}
            onDeleted={file && !liveFile && version === null ? onClose : undefined}
          />
        ) : (
          <p className="muted output-panel__empty">
            {helper ? "The helper is starting on it…" : "This file is not there any more."}
          </p>
        )}
      </div>
    </aside>
  );
}
