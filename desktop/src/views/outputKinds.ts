// How the app shows each kind of file a helper makes (HELPERS_PLAN.md), and
// the page it prints for Export PDF.

export type OutputKind = "markdown" | "html" | "image" | "csv" | "text" | "pdf" | "other";

const KINDS: [RegExp, OutputKind][] = [
  [/\.(md|markdown)$/i, "markdown"],
  [/\.html?$/i, "html"],
  [/\.(png|jpe?g|gif|webp|svg)$/i, "image"],
  [/\.csv$/i, "csv"],
  [/\.(txt|json|log)$/i, "text"],
  [/\.pdf$/i, "pdf"],
];

export function outputKind(name: string): OutputKind {
  return KINDS.find(([pattern]) => pattern.test(name))?.[1] ?? "other";
}

/** Whether Export PDF applies: a page the app renders, Markdown or HTML. A
 * PDF is saved as it is. */
export const printable = (kind: OutputKind) => kind === "markdown" || kind === "html";

const IMAGE_TYPES: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  svg: "image/svg+xml",
};

/** An image's type by its name, so it shows as an image whatever the server
 * said; an SVG only ever loads as an <img>, where its scripts never run. */
export const imageType = (name: string): string =>
  IMAGE_TYPES[name.split(".").pop()?.toLowerCase() ?? ""] ?? "application/octet-stream";

/** Kinds Open hands to the user's own app (outputs.rs keeps the same list):
 * documents, sheets, slides, archives and media, never what runs. */
const OPENABLE = new Set([
  "docx",
  "doc",
  "pptx",
  "ppt",
  "xlsx",
  "xls",
  "odt",
  "odp",
  "ods",
  "rtf",
  "pages",
  "numbers",
  "key",
  "epub",
  "pdf",
  "csv",
  "tsv",
  "txt",
  "md",
  "png",
  "jpg",
  "jpeg",
  "gif",
  "webp",
  "svg",
  "ico",
  "zip",
  "mp3",
  "wav",
  "mp4",
  "mov",
]);

export const openable = (name: string): boolean =>
  name.includes(".") && OPENABLE.has(name.split(".").pop()!.toLowerCase());

/** A file's size in plain units: "12 KB", "3.4 MB". */
export function sizeLabel(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes < 0) return "";
  if (bytes < 1024) return `${bytes} bytes`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

/** The page a presentation moves to on a key, or null when the key leaves
 * it (Esc) or does nothing. Pages count from 1. */
export function presentKey(key: string, page: number, pages: number): number | "leave" | null {
  switch (key) {
    case "Escape":
      return "leave";
    case "ArrowRight":
    case "ArrowDown":
    case "PageDown":
    case " ":
    case "Enter":
      return Math.min(page + 1, pages);
    case "ArrowLeft":
    case "ArrowUp":
    case "PageUp":
    case "Backspace":
      return Math.max(page - 1, 1);
    case "Home":
      return 1;
    case "End":
      return pages;
    default:
      return null;
  }
}

/** The rows of a CSV file, quoted fields and all. */
export function parseCsv(text: string): string[][] {
  const rows: string[][] = [];
  let row: string[] = [];
  let field = "";
  let quoted = false;
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quoted) {
      if (c === '"' && text[i + 1] === '"') {
        field += '"';
        i++;
      } else if (c === '"') quoted = false;
      else field += c;
    } else if (c === '"') quoted = true;
    else if (c === ",") {
      row.push(field);
      field = "";
    } else if (c === "\n" || c === "\r") {
      if (c === "\r" && text[i + 1] === "\n") i++;
      row.push(field);
      rows.push(row);
      row = [];
      field = "";
    } else field += c;
  }
  if (field !== "" || row.length > 0) {
    row.push(field);
    rows.push(row);
  }
  return rows;
}

export const escapeHtml = (text: string) =>
  text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");

/** Plain, printable styles for a page the app makes from a file. */
const PRINT_STYLE = `
  body { font: 15px/1.55 -apple-system, "Segoe UI", system-ui, sans-serif; color: #111; max-width: 46em; margin: 2em auto; padding: 0 1.5em; }
  h1, h2, h3 { line-height: 1.25; break-after: avoid; }
  pre, code { font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 13px; }
  pre { white-space: pre-wrap; background: #f5f5f5; padding: 0.8em; border-radius: 6px; }
  table { border-collapse: collapse; width: 100%; font-size: 13px; }
  th, td { border: 1px solid #ccc; padding: 4px 8px; text-align: left; vertical-align: top; }
  th { background: #f0f0f0; }
  tr { break-inside: avoid; }
  img { max-width: 100%; }
  a { color: inherit; }
  @page { margin: 18mm; }
`;

/** A whole page around body, titled, for the print window. */
export function printPage(title: string, body: string): string {
  return `<!doctype html><html><head><meta charset="utf-8"><title>${escapeHtml(title)}</title><style>${PRINT_STYLE}</style></head><body>${body}</body></html>`;
}

/** A CSV's rows as an HTML table, the first row its header. */
export function csvTable(rows: string[][]): string {
  if (rows.length === 0) return "<p>(empty)</p>";
  const [head, ...rest] = rows;
  const cells = (row: string[], tag: string) =>
    row.map((cell) => `<${tag}>${escapeHtml(cell)}</${tag}>`).join("");
  return `<table><thead><tr>${cells(head, "th")}</tr></thead><tbody>${rest
    .map((row) => `<tr>${cells(row, "td")}</tr>`)
    .join("")}</tbody></table>`;
}
