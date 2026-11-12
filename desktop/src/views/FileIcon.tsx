import type { ReactNode } from "react";
import { type OutputKind, outputKind } from "./outputKinds";

// Outline file icons on a 24px grid, in the text colour, like the rail's:
// a page with a folded corner and a mark for what is in it.

export type FileIconKind = OutputKind | "folder" | "gallery";

const PAGE = <path d="M6 3.5h8l4 4v13H6z M14 3.5v4h4" />;

const MARKS: Record<FileIconKind, ReactNode> = {
  markdown: (
    <>
      {PAGE}
      <path d="M9 12h6M9 15h6M9 18h4" />
    </>
  ),
  text: (
    <>
      {PAGE}
      <path d="M9 12h6M9 15h6M9 18h4" />
    </>
  ),
  html: (
    <>
      {PAGE}
      <path d="M10 12.5l-2 2 2 2M14 12.5l2 2-2 2" />
    </>
  ),
  csv: (
    <>
      {PAGE}
      <path d="M8.5 11.5h7v7h-7zM8.5 15h7M12 11.5v7" />
    </>
  ),
  pdf: (
    <>
      {PAGE}
      <path d="M9 12h6M9 15h6" />
      <rect x="8.5" y="17" width="7" height="2" rx="0.5" />
    </>
  ),
  other: (
    <>
      {PAGE}
      <circle cx="12" cy="15" r="2.5" />
    </>
  ),
  image: (
    <>
      <rect x="4" y="5" width="16" height="14" rx="2" />
      <circle cx="9" cy="10" r="1.5" />
      <path d="M4.5 17l4.5-4.5 3.5 3.5 2.5-2.5 4.5 4.5" />
    </>
  ),
  gallery: (
    <>
      <rect x="6.5" y="7.5" width="14" height="12" rx="2" />
      <path d="M3.5 15.5v-9a2 2 0 0 1 2-2h11" />
      <path d="M7 18l4-4 3 3 2-2 4 4" />
    </>
  ),
  folder: (
    <path d="M3.5 7a1.5 1.5 0 0 1 1.5-1.5h4l2 2h8a1.5 1.5 0 0 1 1.5 1.5v8.5a1.5 1.5 0 0 1-1.5 1.5H5a1.5 1.5 0 0 1-1.5-1.5z" />
  ),
};

/** A file's icon by its kind, or by its name. */
export function FileIcon({ kind, name }: { kind?: FileIconKind; name?: string }) {
  const shown = kind ?? (name ? outputKind(name) : "other");
  return (
    <svg
      className="file-icon"
      viewBox="0 0 24 24"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      {MARKS[shown]}
    </svg>
  );
}
