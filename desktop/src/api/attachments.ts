import { COORDINATOR_BASE, NetworkError, request } from "./client";
import { MAX_DOCUMENT_BYTES, type ProjectDocument, base64Of } from "./documents";
import { fetch } from "@tauri-apps/plugin-http";

// Files attached to a message: pasted screenshots, dropped or picked files.
// They are stored as their owner's documents — a project's, or the top-level
// conversation's ("commitarium") — and the message names them, so whoever
// reads it finds them in its working directory.

/** Where an agent finds a message's files; the coordinator's documents.Dir. */
export const DOCUMENTS_DIR = ".commitarium/documents";
const ATTACHED_PREFIX = "Attached, in your working directory: ";

/** The most files one message carries. */
export const MAX_ATTACHMENTS = 10;

const IMAGE = /\.(png|jpe?g|gif|webp)$/i;

/** Whether a file is an image the agent looks at, and the app shows. */
export const isImageName = (name: string) => IMAGE.test(name);

const pad = (n: number) => String(n).padStart(2, "0");

/** A name for a pasted image with none of its own ("image.png" from the
 * clipboard): screenshot-2026-10-28-1612.png. Other files keep their name,
 * with characters a document name cannot hold replaced. */
export function attachmentName(file: { name: string; type: string }, now: Date): string {
  const pasted = !file.name || /^image\.(png|jpe?g|gif|webp|tiff?)$/i.test(file.name);
  if (pasted && file.type.startsWith("image/")) {
    const extension = file.type === "image/jpeg" ? "jpg" : file.type.slice("image/".length);
    const stamp = `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}-${pad(now.getHours())}${pad(now.getMinutes())}`;
    return `screenshot-${stamp}.${extension}`;
  }
  let name = file.name
    .normalize("NFKD")
    .replace(/[^A-Za-z0-9 ._()-]/g, "-")
    .replace(/\.{2,}/g, ".")
    .replace(/^[^A-Za-z0-9]+/, "");
  if (name.length > 100) {
    const dot = name.lastIndexOf(".");
    const extension = dot > 0 && name.length - dot <= 10 ? name.slice(dot) : "";
    name = name.slice(0, 100 - extension.length) + extension;
  }
  return name || "file";
}

/** Why a file cannot be attached, in plain words; null when it can. */
export function attachmentProblem(file: { name: string; size: number }): string | null {
  if (file.size === 0) return `${file.name || "The file"} is empty.`;
  if (file.size > MAX_DOCUMENT_BYTES) return `${file.name || "The file"} is larger than 5 MB.`;
  return null;
}

interface ClipboardLike {
  items?: ArrayLike<{ kind: string; getAsFile(): File | null }>;
  files?: ArrayLike<File>;
}

/** The files on the clipboard or in a drop: a screenshot copied to the
 * clipboard comes as an image item, a copied or dropped file as a file. */
export function transferredFiles(data: ClipboardLike | null): File[] {
  if (!data) return [];
  const files: File[] = [];
  for (const item of Array.from(data.items ?? [])) {
    if (item.kind !== "file") continue;
    const file = item.getAsFile();
    if (file) files.push(file);
  }
  if (files.length > 0) return files;
  return Array.from(data.files ?? []);
}

/** A message's text without the lines naming its files, and those files —
 * as the coordinator writes them (documents.Attached). */
export function splitAttached(text: string): { text: string; names: string[] } {
  const names: string[] = [];
  const kept: string[] = [];
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed.startsWith(ATTACHED_PREFIX)) {
      kept.push(line);
      continue;
    }
    for (const path of trimmed.slice(ATTACHED_PREFIX.length).split(",")) {
      const p = path.trim();
      if (p.startsWith(DOCUMENTS_DIR + "/")) {
        const name = p.slice(DOCUMENTS_DIR.length + 1);
        if (name && !names.includes(name)) names.push(name);
      }
    }
  }
  return { text: names.length ? kept.join("\n").trimEnd() : text, names };
}

const attachmentsPath = (owner: string) => `/api/v1/attachments/${encodeURIComponent(owner)}`;

/** Store a file attached to a message; a taken name gets a number. */
export const uploadAttachment = async (
  owner: string,
  file: Blob,
  name: string,
): Promise<ProjectDocument> =>
  request(attachmentsPath(owner), {
    method: "POST",
    body: { name, content_base64: await base64Of(file) },
  });

/** An attached file's contents, to show an image or open a file. */
export async function readAttachment(owner: string, name: string): Promise<Blob> {
  let response: Response;
  try {
    response = await fetch(
      `${COORDINATOR_BASE}${attachmentsPath(owner)}/${encodeURIComponent(name)}`,
    );
  } catch (e) {
    throw new NetworkError(`cannot reach coordinator: ${String(e)}`);
  }
  if (!response.ok) throw new Error("The file is not there any more.");
  return response.blob();
}
