import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { getCurrentWindow } from "@tauri-apps/api/window";
import type { PDFDocumentProxy } from "pdfjs-dist/legacy/build/pdf.mjs";
import { presentKey } from "./outputKinds";

// A helper's PDF, drawn page by page with pdf.js onto canvases: nothing in
// the file runs (no scripts, no forms, no links), and pdf.js loads only when
// a PDF is opened. Present shows it full screen, a page at a time.

let library: Promise<typeof import("pdfjs-dist/legacy/build/pdf.mjs")> | null = null;

function pdfjs() {
  library ??= Promise.all([
    import("pdfjs-dist/legacy/build/pdf.mjs"),
    import("pdfjs-dist/legacy/build/pdf.worker.min.mjs?url"),
  ]).then(([lib, worker]) => {
    lib.GlobalWorkerOptions.workerSrc = worker.default;
    return lib;
  });
  return library;
}

/** The PDF in blob, once pdf.js has read it. */
export function usePdf(blob: Blob): { doc: PDFDocumentProxy | null; error: string | null } {
  const [state, setState] = useState<{ doc: PDFDocumentProxy | null; error: string | null }>({
    doc: null,
    error: null,
  });
  useEffect(() => {
    let gone = false;
    let task: { destroy: () => Promise<void> } | null = null;
    (async () => {
      const lib = await pdfjs();
      const data = new Uint8Array(await blob.arrayBuffer());
      if (gone) return;
      const loading = lib.getDocument({ data, enableXfa: false, stopAtErrors: false });
      task = loading;
      const doc = await loading.promise;
      if (!gone) setState({ doc, error: null });
    })().catch((e) => {
      if (!gone) setState({ doc: null, error: e instanceof Error ? e.message : String(e) });
    });
    return () => {
      gone = true;
      void task?.destroy();
    };
  }, [blob]);
  return state;
}

/** Draws page number onto canvas, to fit width × height (CSS pixels);
 * aborting stops a drawing still under way. */
async function drawPage(
  doc: PDFDocumentProxy,
  number: number,
  canvas: HTMLCanvasElement,
  width: number,
  height = Infinity,
  signal?: AbortSignal,
) {
  const page = await doc.getPage(number);
  if (signal?.aborted) return;
  const natural = page.getViewport({ scale: 1 });
  const scale = Math.min(width / natural.width, height / natural.height);
  const viewport = page.getViewport({ scale });
  const ratio = window.devicePixelRatio || 1;
  canvas.width = Math.floor(viewport.width * ratio);
  canvas.height = Math.floor(viewport.height * ratio);
  canvas.style.width = `${Math.floor(viewport.width)}px`;
  canvas.style.height = `${Math.floor(viewport.height)}px`;
  const task = page.render({
    canvas,
    viewport,
    transform: ratio === 1 ? undefined : [ratio, 0, 0, ratio, 0, 0],
  });
  signal?.addEventListener("abort", () => task.cancel());
  await task.promise;
}

/** Every page, one under the other, as wide as the viewer. */
export function PdfPages({ doc }: { doc: PDFDocumentProxy }) {
  const holder = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const element = holder.current;
    if (!element) return;
    const drawing = new AbortController();
    element.replaceChildren();
    const width = Math.max(320, element.clientWidth - 2);
    (async () => {
      for (let number = 1; number <= doc.numPages && !drawing.signal.aborted; number++) {
        const canvas = document.createElement("canvas");
        canvas.className = "pdf-pages__page";
        element.appendChild(canvas);
        await drawPage(doc, number, canvas, width, Infinity, drawing.signal);
      }
    })().catch(() => undefined);
    return () => drawing.abort();
  }, [doc]);
  return <div className="pdf-pages" ref={holder} />;
}

/** The PDF full screen, a page at a time: arrows, space or a click move
 * on (a click on the left third goes back), Esc leaves. */
export function PdfPresenter({ doc, onLeave }: { doc: PDFDocumentProxy; onLeave: () => void }) {
  const [page, setPage] = useState(1);
  const [size, setSize] = useState({ width: window.innerWidth, height: window.innerHeight });
  const canvas = useRef<HTMLCanvasElement>(null);
  const pages = doc.numPages;

  // The window goes full screen while presenting, unless it already was.
  useEffect(() => {
    const appWindow = getCurrentWindow();
    let restore = false;
    appWindow
      .isFullscreen()
      .then((already) => {
        if (already) return;
        restore = true;
        return appWindow.setFullscreen(true);
      })
      .catch(() => undefined);
    return () => {
      if (restore) void appWindow.setFullscreen(false).catch(() => undefined);
    };
  }, []);

  useEffect(() => {
    const onResize = () => setSize({ width: window.innerWidth, height: window.innerHeight });
    // Before the viewer's own Escape, which would close it too.
    const onKey = (e: KeyboardEvent) => {
      const next = presentKey(e.key, page, pages);
      if (next === null) return;
      e.preventDefault();
      e.stopPropagation();
      if (next === "leave") onLeave();
      else setPage(next);
    };
    window.addEventListener("resize", onResize);
    window.addEventListener("keydown", onKey, true);
    return () => {
      window.removeEventListener("resize", onResize);
      window.removeEventListener("keydown", onKey, true);
    };
  }, [page, pages, onLeave]);

  useEffect(() => {
    if (!canvas.current) return;
    const drawing = new AbortController();
    drawPage(doc, page, canvas.current, size.width, size.height, drawing.signal).catch(
      () => undefined,
    );
    return () => drawing.abort();
  }, [doc, page, size]);

  return createPortal(
    <div
      className="pdf-present"
      role="dialog"
      aria-label={`Page ${page} of ${pages}`}
      onClick={(e) => {
        const back = e.clientX < window.innerWidth / 3;
        setPage((current) => (back ? Math.max(current - 1, 1) : Math.min(current + 1, pages)));
      }}
    >
      <canvas ref={canvas} className="pdf-present__page" />
      <span className="pdf-present__count" aria-hidden>
        {page} / {pages}
      </span>
    </div>,
    document.body,
  );
}
