/** Where a release's own text ends: the download page's installer notes
 * follow it in the release body, and the updater gets the whole body. */
export const NOTES_END = "<!-- installer -->";

// Releases before NOTES_END had only the download page's text, which says
// nothing about what changed.
const DOWNLOAD_PAGE = [
  "Download the installer for your operating system below.",
  "Docker Desktop (with Docker Compose) is required.",
];

/** What changed, from an update's notes: the text before NOTES_END, without
 * the download page's lines; "" when there is nothing else. */
export function whatsNew(notes: string | null | undefined): string {
  if (!notes) return "";
  const own = notes.split(NOTES_END)[0];
  return own
    .split("\n")
    .filter((line) => !DOWNLOAD_PAGE.some((text) => line.trim().startsWith(text)))
    .join("\n")
    .trim();
}

/** A release's date as the reader writes dates. The updater gives it as
 * "2026-11-07 17:55:26.439 +00:00:00", which Date cannot read. */
export function releaseDate(published: string): string {
  const day = /^(\d{4})-(\d{2})-(\d{2})/.exec(published);
  const date = day
    ? new Date(Number(day[1]), Number(day[2]) - 1, Number(day[3]))
    : new Date(published);
  return isNaN(date.getTime()) ? "" : date.toLocaleDateString();
}
