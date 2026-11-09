import type { HelperFile, OutputFile, OutputVersion } from "../api/outputs";
import { outputKind } from "./outputKinds";

// What the panel beside the chat shows (HELPERS_PLAN.md, H8): an output the
// user opened, or a helper's file while it is built, which becomes the
// stored output once the helper hands it back.

/** The panel's subject: a stored output by name, or a helper's file by its
 * own name while it builds it. */
export interface PanelTarget {
  name?: string;
  helper?: string;
  live?: string;
  /** Images to step through, in order, starting at the first. */
  images?: string[];
}

/** The stored output the target is, once there is one. */
export function storedFor(target: PanelTarget, files: OutputFile[]): OutputFile | null {
  if (target.name) return files.find((file) => file.name === target.name) ?? null;
  if (!target.helper || !target.live) return null;
  return (
    files.find(
      (file) => file.helper === target.helper && (file.source ?? file.name) === target.live,
    ) ?? null
  );
}

/** The file in the helper's folder that is this output as it is rebuilt:
 * the one of its own name, or, before it has any, the target's. */
export function liveFor(
  target: PanelTarget,
  file: OutputFile | null,
  live: HelperFile[],
): HelperFile | null {
  const want = file ? (file.source ?? file.name) : target.live;
  return live.find((candidate) => candidate.name === want) ?? null;
}

/** The helper whose folder the panel follows: the one at work on the file
 * now, if any. */
export function liveHelper(
  target: PanelTarget,
  file: OutputFile | null,
  atWork: ReadonlySet<string>,
): string | null {
  const helper = file?.helper ?? target.helper;
  return helper && atWork.has(helper) ? helper : null;
}

/** Changes whenever what the panel shows changes, so it reads it again. */
export function shownKey(
  file: OutputFile | null,
  version: number | null,
  live: HelperFile | null,
): string {
  if (live) return `live:${live.name}:${live.size}:${live.made_at}`;
  if (!file) return "none";
  return `stored:${file.name}:${version ?? "latest"}:${file.version ?? 1}:${file.made_at}`;
}

/** The first helper at work that has made a file and the panel has not
 * opened on before: the panel opens on its first file by itself. */
export function autoOpenTarget(
  atWork: string[],
  seen: ReadonlySet<string>,
  filesOf: ReadonlyMap<string, HelperFile[]>,
): PanelTarget | null {
  for (const helper of atWork) {
    const files = filesOf.get(helper);
    if (seen.has(helper) || !files?.length) continue;
    return { helper, live: files[0].name };
  }
  return null;
}

/** A version as the history lists it. */
export function versionLabel(version: OutputVersion, latest: number): string {
  return version.number === latest ? `v${version.number} · latest` : `v${version.number}`;
}

/** The panel's title. */
export function panelTitle(target: PanelTarget, file: OutputFile | null): string {
  return file?.name ?? target.name ?? target.live ?? "File";
}

/** Past this many files, a helper's files fold: its documents stay one by
 * one, and its images become one entry that steps through them. */
export const FOLD_OVER = 4;

/** A helper's files as the chat and the Outputs card show them: documents
 * first, then its images folded into one gallery when there are many. */
export function foldOutputs(names: string[]): { documents: string[]; images: string[] } {
  const images = names.filter((name) => outputKind(name) === "image");
  if (names.length <= FOLD_OVER || images.length < 2) return { documents: names, images: [] };
  return {
    documents: names.filter((name) => outputKind(name) !== "image"),
    images: [...images].sort((a, b) => a.localeCompare(b, undefined, { numeric: true })),
  };
}

/** The gallery's name: "20 screenshots" for a test's numbered shots, else
 * "N images". */
export function imagesLabel(images: string[]): string {
  const shots = images.every((name) => /^\d|screenshot|screen-shot/i.test(name));
  return `${images.length} ${shots ? "screenshots" : "images"}`;
}

/** A row of the Outputs card: one file, or one helper's run of many. */
export type OutputRow =
  | { kind: "file"; file: OutputFile }
  | { kind: "run"; key: string; title: string; files: OutputFile[]; madeAt: string };

/** The Outputs card's rows, newest first: a helper's files one by one,
 * or, past FOLD_OVER, as one row for its run. */
export function outputRows(files: OutputFile[]): OutputRow[] {
  const runs = new Map<string, OutputFile[]>();
  for (const file of files) {
    if (!file.helper) continue;
    runs.set(file.helper, [...(runs.get(file.helper) ?? []), file]);
  }
  const rows: OutputRow[] = [];
  const placed = new Set<string>();
  for (const file of files) {
    const run = file.helper ? (runs.get(file.helper) ?? []) : [];
    if (run.length <= FOLD_OVER) {
      rows.push({ kind: "file", file });
      continue;
    }
    if (placed.has(file.helper as string)) continue;
    placed.add(file.helper as string);
    rows.push({
      kind: "run",
      key: file.helper as string,
      title: file.title || "A helper's files",
      files: run,
      madeAt: file.made_at,
    });
  }
  return rows;
}
