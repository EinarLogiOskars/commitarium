import { useCallback, useEffect, useState } from "react";
import ReactMarkdown from "react-markdown";
import { renderToStaticMarkup } from "react-dom/server";
import remarkGfm from "remark-gfm";
import {
  OUTPUTS_CHANGED,
  type OutputFile,
  deleteOutput,
  listOutputs,
  printOutput,
  readOutput,
  saveOutput,
} from "../api/outputs";
import { confirmAction } from "../confirm";
import { Markdown } from "./Markdown";
import { Overlay } from "./Overlay";
import {
  type OutputKind,
  csvTable,
  escapeHtml,
  outputKind,
  parseCsv,
  printPage,
  printable,
} from "./outputKinds";

// What Mayka's helpers make (HELPERS_PLAN.md): chips on the chat line that
// hands them back, the Outputs card, and a viewer with Export PDF and Save.

const POLL_MS = 15_000;

const describe = (e: unknown) => (e instanceof Error ? e.message : String(e));
const SHOWN = 5;

const ICON: Record<OutputKind, string> = {
  markdown: "📄",
  html: "🖥",
  image: "🖼",
  csv: "📊",
  text: "📄",
  pdf: "📕",
  other: "📦",
};

/** A helper's files, as chips that open the viewer. */
export function OutputChips({ owner, names }: { owner: string | null; names?: string[] }) {
  const [open, setOpen] = useState<string | null>(null);
  if (!owner || !names?.length) return null;
  return (
    <>
      <ul className="attachments attachments--sent outputs-chips">
        {names.map((name) => (
          <li key={name}>
            <button
              className="attachment output-chip"
              title={`Open ${name}`}
              onClick={() => setOpen(name)}
            >
              <span className="attachment__icon" aria-hidden>
                {ICON[outputKind(name)]}
              </span>
              <span className="attachment__name">{name}</span>
            </button>
          </li>
        ))}
      </ul>
      {open && <OutputViewer owner={owner} name={open} onClose={() => setOpen(null)} />}
    </>
  );
}

/** The scope's outputs, newest first; shown only once there are any. */
export function OutputsCard({ owner }: { owner: string }) {
  const [files, setFiles] = useState<OutputFile[]>([]);
  const [open, setOpen] = useState<string | null>(null);
  const [showAll, setShowAll] = useState(false);
  const load = useCallback(() => {
    listOutputs(owner)
      .then((r) => setFiles(r.outputs ?? []))
      // An older coordinator has no outputs: the card stays hidden.
      .catch(() => setFiles([]));
  }, [owner]);
  useEffect(() => {
    load();
    const timer = setInterval(load, POLL_MS);
    window.addEventListener(OUTPUTS_CHANGED, load);
    return () => {
      clearInterval(timer);
      window.removeEventListener(OUTPUTS_CHANGED, load);
    };
  }, [load]);
  if (files.length === 0) return null;
  const row = (file: OutputFile) => (
    <li key={file.name}>
      <button
        className="output-row"
        title={file.title ? `${file.name}, for “${file.title}”` : file.name}
        onClick={() => {
          setShowAll(false);
          setOpen(file.name);
        }}
      >
        <span aria-hidden>{ICON[outputKind(file.name)]}</span>
        <span className="output-row__name">{file.name}</span>
        <span className="muted output-row__when">{madeWhen(file.made_at)}</span>
      </button>
    </li>
  );
  return (
    <section className="shell-card">
      <div className="shell-card__top">
        <h2 className="shell-card__head">Outputs</h2>
        <span className="muted shell-card__meta">{files.length}</span>
      </div>
      <ul className="output-rows">{files.slice(0, SHOWN).map(row)}</ul>
      {files.length > SHOWN && (
        <button className="shell-card__link" onClick={() => setShowAll(true)}>
          All ({files.length})
        </button>
      )}
      {showAll && (
        <Overlay title="Outputs" onClose={() => setShowAll(false)}>
          <ul className="output-rows">{files.map(row)}</ul>
        </Overlay>
      )}
      {open && (
        <OutputViewer
          owner={owner}
          name={open}
          onClose={() => setOpen(null)}
          onDeleted={() => {
            setOpen(null);
            load();
          }}
        />
      )}
    </section>
  );
}

function madeWhen(at: string): string {
  const date = new Date(at);
  if (Number.isNaN(date.getTime())) return "";
  const today = new Date();
  return date.toDateString() === today.toDateString()
    ? date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })
    : date.toLocaleDateString([], { month: "short", day: "numeric" });
}

interface Loaded {
  blob: Blob;
  text?: string;
  url?: string;
}

/** One output, shown as the app can, with Export PDF, Save, and Delete. */
export function OutputViewer({
  owner,
  name,
  onClose,
  onDeleted,
}: {
  owner: string;
  name: string;
  onClose: () => void;
  onDeleted?: () => void;
}) {
  const kind = outputKind(name);
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let gone = false;
    let url: string | undefined;
    readOutput(owner, name)
      .then(async (blob) => {
        const text = ["markdown", "html", "csv", "text"].includes(kind)
          ? await blob.text()
          : undefined;
        if (kind === "image" || kind === "pdf") url = URL.createObjectURL(blob);
        if (!gone) setLoaded({ blob, text, url });
      })
      .catch((e) => !gone && setError(describe(e)));
    return () => {
      gone = true;
      if (url) URL.revokeObjectURL(url);
    };
  }, [owner, name, kind]);

  const act = (work: () => Promise<unknown>) => {
    setBusy(true);
    setError(null);
    work()
      .catch((e) => setError(describe(e)))
      .finally(() => setBusy(false));
  };
  const exportPdf = () =>
    act(async () => {
      if (!loaded) return;
      await printOutput(name.replace(/\.[^.]+$/, ""), await printableHtml(name, kind, loaded));
    });
  const save = () => act(async () => loaded && saveOutput(name, loaded.blob));
  const remove = () =>
    act(async () => {
      if (!(await confirmAction(`Delete ${name}? This cannot be undone.`, "Delete"))) return;
      await deleteOutput(owner, name);
      window.dispatchEvent(new Event(OUTPUTS_CHANGED));
      (onDeleted ?? onClose)();
    });

  return (
    <Overlay title={name} onClose={onClose} wide>
      <div className="output-viewer__actions">
        {printable(kind) && (
          <button className="primary" onClick={exportPdf} disabled={!loaded || busy}>
            Export PDF
          </button>
        )}
        <button onClick={save} disabled={!loaded || busy}>
          Save…
        </button>
        <button className="ghost" onClick={remove} disabled={busy}>
          Delete
        </button>
        {error && <span className="chat-view__error">{error}</span>}
      </div>
      <div className="output-viewer__body">
        {!loaded ? (
          !error && <p className="muted">Loading…</p>
        ) : (
          <OutputBody kind={kind} name={name} loaded={loaded} />
        )}
      </div>
    </Overlay>
  );
}

function OutputBody({ kind, name, loaded }: { kind: OutputKind; name: string; loaded: Loaded }) {
  switch (kind) {
    case "markdown":
      return (
        <div className="output-viewer__page">
          <Markdown text={loaded.text ?? ""} />
        </div>
      );
    case "html":
      // A helper's page never runs in the app: no scripts, no same origin.
      return (
        <iframe className="output-viewer__frame" title={name} sandbox="" srcDoc={loaded.text} />
      );
    case "image":
      return <img className="output-viewer__image" src={loaded.url} alt={name} />;
    case "csv":
      return (
        <div
          className="output-viewer__page output-viewer__table"
          // csvTable escapes every cell.
          dangerouslySetInnerHTML={{ __html: csvTable(parseCsv(loaded.text ?? "")) }}
        />
      );
    case "text":
      return <pre className="output-viewer__text">{loaded.text}</pre>;
    case "pdf":
      return <iframe className="output-viewer__frame" title={name} src={loaded.url} />;
    default:
      return (
        <p className="muted">Commitarium cannot show this kind of file. Save it to open it.</p>
      );
  }
}

/** The page Export PDF prints: the file as the app shows it, on plain paper
 * styles; a helper's HTML as it is. */
async function printableHtml(name: string, kind: OutputKind, loaded: Loaded): Promise<string> {
  switch (kind) {
    case "html":
      return loaded.text ?? "";
    case "markdown":
      return printPage(
        name,
        renderToStaticMarkup(
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{loaded.text ?? ""}</ReactMarkdown>,
        ),
      );
    case "csv":
      return printPage(name, csvTable(parseCsv(loaded.text ?? "")));
    case "image":
      return printPage(name, `<img src="${await dataUrl(loaded.blob)}" alt="${escapeHtml(name)}">`);
    default:
      return printPage(name, `<pre>${escapeHtml(loaded.text ?? "")}</pre>`);
  }
}

function dataUrl(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(reader.error);
    reader.readAsDataURL(blob);
  });
}
