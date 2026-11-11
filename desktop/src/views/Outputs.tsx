import { type ReactNode, useCallback, useEffect, useState } from "react";
import ReactMarkdown from "react-markdown";
import { renderToStaticMarkup } from "react-dom/server";
import remarkGfm from "remark-gfm";
import {
  OUTPUTS_CHANGED,
  type OutputFile,
  deleteOutput,
  listOutputs,
  openOutput,
  openOutputBeside,
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
import { type OutputRow, foldOutputs, imagesLabel, outputRows, shownOutputs } from "./panelTargets";
import { ImagePresenter, PdfPages, PdfPresenter, usePdf } from "./PdfView";
import { FileIcon } from "./FileIcon";

// What Mayka's helpers make (HELPERS_PLAN.md): chips on the chat line that
// hands them back, the Outputs card, and a viewer with Export PDF, Present
// (PDFs), Open (what it cannot show) and Save.

const POLL_MS = 15_000;

const describe = (e: unknown) => (e instanceof Error ? e.message : String(e));
const SHOWN = 5;

/** A helper's files, as chips that open the viewer: beside the chat when
 * onOpen opens it there, else over the page. Many images fold into one chip
 * that steps through them (foldOutputs). */
export function OutputChips({
  owner,
  names,
  onOpen,
  onOpenImages,
}: {
  owner: string | null;
  names?: string[];
  onOpen?: (name: string) => void;
  onOpenImages?: (images: string[]) => void;
}) {
  const [open, setOpen] = useState<string | null>(null);
  const [gallery, setGallery] = useState<string[] | null>(null);
  if (!owner || !names?.length) return null;
  const { documents, images } = foldOutputs(names);
  const chip = (key: string, icon: ReactNode, label: string, title: string, click: () => void) => (
    <li key={key}>
      <button className="attachment output-chip" title={title} onClick={click}>
        <span className="attachment__icon" aria-hidden>
          {icon}
        </span>
        <span className="attachment__name">{label}</span>
      </button>
    </li>
  );
  return (
    <>
      <ul className="attachments attachments--sent outputs-chips">
        {documents.map((name) =>
          chip(name, <FileIcon name={name} />, name, `Open ${name}`, () =>
            onOpen ? onOpen(name) : setOpen(name),
          ),
        )}
        {images.length > 0 &&
          chip(
            "images",
            <FileIcon kind="gallery" />,
            imagesLabel(images),
            "Step through them",
            () => (onOpenImages ? onOpenImages(images) : setGallery(images)),
          )}
      </ul>
      {open && <OutputViewer owner={owner} name={open} onClose={() => setOpen(null)} />}
      {gallery && (
        <Overlay title={imagesLabel(gallery)} onClose={() => setGallery(null)} wide>
          <OutputGallery owner={owner} images={gallery} />
        </Overlay>
      )}
    </>
  );
}

/** The scope's outputs, newest first; shown only once there are any. A
 * helper's run of many files is one row that unfolds. A file opens beside
 * the chat when one of its owner is showing, else over the page. */
export function OutputsCard({ owner }: { owner: string }) {
  const [files, setFiles] = useState<OutputFile[]>([]);
  const [open, setOpen] = useState<string | null>(null);
  const [gallery, setGallery] = useState<string[] | null>(null);
  const [showAll, setShowAll] = useState(false);
  const [unfolded, setUnfolded] = useState<Set<string>>(new Set());
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
  const rows = outputRows(files);
  const shownCount = shownOutputs(files).length;
  const fileRow = (file: OutputFile, nested = false) => (
    <li key={file.name}>
      <button
        className={`output-row ${nested ? "output-row--nested" : ""}`}
        title={file.title ? `${file.name}, for “${file.title}”` : file.name}
        onClick={() => {
          setShowAll(false);
          if (!openOutputBeside(owner, file.name)) setOpen(file.name);
        }}
      >
        <FileIcon name={file.name} />
        <span className="output-row__name">{file.name}</span>
        {(file.version ?? 1) > 1 && (
          <span className="muted output-row__version">v{file.version}</span>
        )}
        <span className="muted output-row__when">{madeWhen(file.made_at)}</span>
      </button>
    </li>
  );
  const row = (entry: OutputRow) => {
    if (entry.kind === "file") return fileRow(entry.file);
    const isOpen = unfolded.has(entry.key);
    const { documents, images } = foldOutputs(entry.files.map((file) => file.name));
    return (
      <li key={entry.key}>
        <button
          className="output-row"
          aria-expanded={isOpen}
          onClick={() =>
            setUnfolded((was) => {
              const next = new Set(was);
              if (isOpen) next.delete(entry.key);
              else next.add(entry.key);
              return next;
            })
          }
        >
          <span aria-hidden>{isOpen ? "▾" : "▸"}</span>
          <span className="output-row__name">{entry.title}</span>
          <span className="muted output-row__version">{entry.files.length} files</span>
          <span className="muted output-row__when">{madeWhen(entry.madeAt)}</span>
        </button>
        {isOpen && (
          <ul className="output-rows output-rows--nested">
            {documents.map((name) => {
              const file = entry.files.find((f) => f.name === name);
              return file ? fileRow(file, true) : null;
            })}
            {images.length > 0 && (
              <li>
                <button
                  className="output-row output-row--nested"
                  onClick={() => {
                    setShowAll(false);
                    if (!openOutputBeside(owner, undefined, images)) setGallery(images);
                  }}
                >
                  <FileIcon kind="gallery" />
                  <span className="output-row__name">{imagesLabel(images)}</span>
                </button>
              </li>
            )}
          </ul>
        )}
      </li>
    );
  };
  return (
    <section className="shell-card">
      <div className="shell-card__top">
        <h2 className="shell-card__head">Outputs</h2>
        <span className="muted shell-card__meta">{shownCount}</span>
      </div>
      <ul className="output-rows">{rows.slice(0, SHOWN).map(row)}</ul>
      {rows.length > SHOWN && (
        <button className="shell-card__link" onClick={() => setShowAll(true)}>
          All ({shownCount})
        </button>
      )}
      {showAll && (
        <Overlay title="Outputs" onClose={() => setShowAll(false)}>
          <ul className="output-rows">{rows.map(row)}</ul>
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
      {gallery && (
        <Overlay title={imagesLabel(gallery)} onClose={() => setGallery(null)} wide>
          <OutputGallery owner={owner} images={gallery} />
        </Overlay>
      )}
    </section>
  );
}

/** Images stepped through one at a time, in order: a test's screenshots.
 * Each loads as an <img> from a blob of its own type, so nothing in it runs. */
export function OutputGallery({ owner, images }: { owner: string; images: string[] }) {
  const [index, setIndex] = useState(0);
  const [presenting, setPresenting] = useState(false);
  const [shown, setShown] = useState<{ url: string; blob: Blob } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const name = images[Math.min(index, images.length - 1)];
  useEffect(() => {
    let gone = false;
    let url: string | undefined;
    readOutput(owner, name)
      .then((blob) => {
        url = URL.createObjectURL(new Blob([blob], { type: imageType(name) }));
        if (!gone) {
          setShown({ url, blob });
          setError(null);
        }
      })
      .catch((e) => !gone && setError(describe(e)));
    return () => {
      gone = true;
      if (url) setTimeout(() => URL.revokeObjectURL(url as string), 1000);
    };
  }, [owner, name]);
  const step = useCallback(
    (by: number) => setIndex((at) => Math.max(0, Math.min(images.length - 1, at + by))),
    [images.length],
  );
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.target instanceof HTMLElement && e.target.closest("input, textarea")) return;
      if (e.key === "ArrowRight") step(1);
      if (e.key === "ArrowLeft") step(-1);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [step]);
  return (
    <>
      <div className="output-viewer__actions">
        <button onClick={() => step(-1)} disabled={index === 0} aria-label="Previous">
          ←
        </button>
        <span className="muted">
          {index + 1} of {images.length}
        </span>
        <button onClick={() => step(1)} disabled={index >= images.length - 1} aria-label="Next">
          →
        </button>
        <span className="output-gallery__name" title={name}>
          {name}
        </span>
        <button className="primary" onClick={() => setPresenting(true)}>
          Present
        </button>
        <button
          onClick={() =>
            shown && void saveOutput(name, shown.blob).catch((e) => setError(describe(e)))
          }
          disabled={!shown}
        >
          Save…
        </button>
        {error && <span className="chat-view__error">{error}</span>}
      </div>
      <div className="output-viewer__body">
        {shown ? (
          <img className="output-viewer__image" src={shown.url} alt={name} />
        ) : (
          !error && <p className="muted">Loading…</p>
        )}
      </div>
      {presenting && (
        <ImagePresenter
          url={shown?.url ?? null}
          index={index}
          count={images.length}
          onStep={setIndex}
          onLeave={() => setPresenting(false)}
        />
      )}
    </>
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

/** One output over the page, as the app can show it, with Export PDF,
 * Present, Open, Save and Delete; where there is no chat beside it. */
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
  const load = useCallback(() => readOutput(owner, name), [owner, name]);
  return (
    <Overlay title={name} onClose={onClose} wide>
      <OutputView
        owner={owner}
        name={name}
        load={load}
        shown={name}
        onDeleted={onDeleted ?? onClose}
      />
    </Overlay>
  );
}

/** A file as the app can show it, with its actions: the viewer of the
 * overlay and of the panel beside the chat. load reads it; it reads again
 * whenever shown changes, keeping what it showed until the new one is in.
 * Without onDeleted it cannot be deleted (a helper's file being built). */
export function OutputView({
  owner,
  name,
  load,
  shown,
  file: known,
  onDeleted,
  actions,
}: {
  owner: string;
  name: string;
  load: () => Promise<Blob>;
  shown: string;
  file?: OutputFile | null;
  onDeleted?: () => void;
  actions?: ReactNode;
}) {
  const kind = outputKind(name);
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let gone = false;
    let url: string | undefined;
    load()
      .then(async (blob) => {
        const text = ["markdown", "html", "csv", "text"].includes(kind)
          ? await blob.text()
          : undefined;
        // An image, an SVG too, loads only as an <img> from a blob of its
        // own type: its scripts never run.
        if (kind === "image")
          url = URL.createObjectURL(new Blob([blob], { type: imageType(name) }));
        if (!gone) {
          setLoaded({ blob, text, url });
          setError(null);
        }
      })
      .catch((e) => !gone && setError(describe(e)));
    return () => {
      gone = true;
      if (url) setTimeout(() => URL.revokeObjectURL(url as string), 1000);
    };
    // shown says when to read it again.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shown, kind, name]);
  // Who made a file the app cannot show, and when, for its details.
  const [listed, setFile] = useState<OutputFile | null>(null);
  useEffect(() => {
    if (kind !== "other" || known !== undefined) return;
    let gone = false;
    listOutputs(owner)
      .then((r) => !gone && setFile(r.outputs?.find((f) => f.name === name) ?? null))
      .catch(() => undefined);
    return () => {
      gone = true;
    };
  }, [owner, name, kind, known]);
  const file = known !== undefined ? known : listed;
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
      onDeleted?.();
    });

  return (
    <>
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
        {onDeleted && (
          <button className="ghost" onClick={remove} disabled={busy}>
            Delete
          </button>
        )}
        {actions}
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
    </>
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
