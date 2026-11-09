import { useCallback, useEffect, useState } from "react";
import ReactMarkdown from "react-markdown";
import { renderToStaticMarkup } from "react-dom/server";
import remarkGfm from "remark-gfm";
import {
  OUTPUTS_CHANGED,
  type OutputFile,
  deleteOutput,
  listOutputs,
  openOutput,
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
  imageType,
  openable,
  outputKind,
  parseCsv,
  printPage,
  printable,
  sizeLabel,
} from "./outputKinds";
import { PdfPages, PdfPresenter, usePdf } from "./PdfView";

// What Mayka's helpers make (HELPERS_PLAN.md): chips on the chat line that
// hands them back, the Outputs card, and a viewer with Export PDF, Present
// (PDFs), Open (what it cannot show) and Save.

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
        // An image, an SVG too, loads only as an <img> from a blob of its
        // own type: its scripts never run.
        if (kind === "image")
          url = URL.createObjectURL(new Blob([blob], { type: imageType(name) }));
        if (!gone) setLoaded({ blob, text, url });
      })
      .catch((e) => !gone && setError(describe(e)));
    return () => {
      gone = true;
      if (url) URL.revokeObjectURL(url);
    };
  }, [owner, name, kind]);
  // Who made a file the app cannot show, and when, for its details.
  const [file, setFile] = useState<OutputFile | null>(null);
  useEffect(() => {
    if (kind !== "other") return;
    let gone = false;
    listOutputs(owner)
      .then((r) => !gone && setFile(r.outputs?.find((f) => f.name === name) ?? null))
      .catch(() => undefined);
    return () => {
      gone = true;
    };
  }, [owner, name, kind]);
  const [presenting, setPresenting] = useState(false);

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
      await printOutput(name.replace(/\.[^.]+$/, ""), printableHtml(name, kind, loaded));
    });
  const save = () => act(async () => loaded && saveOutput(name, loaded.blob));
  const open = () => act(async () => loaded && openOutput(name, loaded.blob));
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
        {kind === "pdf" && (
          <button className="primary" onClick={() => setPresenting(true)} disabled={!loaded}>
            Present
          </button>
        )}
        {kind === "other" && openable(name) && (
          <button className="primary" onClick={open} disabled={!loaded || busy}>
            Open
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
          <OutputBody
            kind={kind}
            name={name}
            loaded={loaded}
            file={file}
            presenting={presenting}
            onLeave={() => setPresenting(false)}
          />
        )}
      </div>
    </Overlay>
  );
}

function OutputBody({
  kind,
  name,
  loaded,
  file,
  presenting,
  onLeave,
}: {
  kind: OutputKind;
  name: string;
  loaded: Loaded;
  file: OutputFile | null;
  presenting: boolean;
  onLeave: () => void;
}) {
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
      return <PdfBody blob={loaded.blob} presenting={presenting} onLeave={onLeave} />;
    default:
      return <OutputDetails name={name} size={file?.size ?? loaded.blob.size} file={file} />;
  }
}

function PdfBody({
  blob,
  presenting,
  onLeave,
}: {
  blob: Blob;
  presenting: boolean;
  onLeave: () => void;
}) {
  const { doc, error } = usePdf(blob);
  if (error)
    return <p className="muted">This PDF could not be shown: {error}. Save it to open it.</p>;
  if (!doc) return <p className="muted">Loading…</p>;
  return (
    <>
      <PdfPages doc={doc} />
      {presenting && <PdfPresenter doc={doc} onLeave={onLeave} />}
    </>
  );
}

/** A file the app cannot show: what it is, who made it and when. */
function OutputDetails({
  name,
  size,
  file,
}: {
  name: string;
  size: number;
  file: OutputFile | null;
}) {
  const made = file ? new Date(file.made_at) : null;
  return (
    <div className="output-viewer__details">
      <p className="output-viewer__details-name">{name}</p>
      <dl>
        <dt>Size</dt>
        <dd>{sizeLabel(size)}</dd>
        {file?.title && (
          <>
            <dt>Made by</dt>
            <dd>The helper on “{file.title}”</dd>
          </>
        )}
        {made && !Number.isNaN(made.getTime()) && (
          <>
            <dt>Made</dt>
            <dd>{made.toLocaleString([], { dateStyle: "medium", timeStyle: "short" })}</dd>
          </>
        )}
      </dl>
      <p className="muted">
        {openable(name)
          ? "Commitarium cannot show this kind of file here. Open it in your own app, or save it."
          : "Commitarium cannot show this kind of file. Save it to open it."}
      </p>
    </div>
  );
}

/** The page Export PDF prints: a helper's HTML as it is, or its Markdown
 * on plain paper styles. */
function printableHtml(name: string, kind: OutputKind, loaded: Loaded): string {
  return kind === "html"
    ? (loaded.text ?? "")
    : printPage(
        name,
        renderToStaticMarkup(
          <ReactMarkdown remarkPlugins={[remarkGfm]}>{loaded.text ?? ""}</ReactMarkdown>,
        ),
      );
}
