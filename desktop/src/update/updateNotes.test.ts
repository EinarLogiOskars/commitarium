import { describe, expect, it } from "vitest";
import { NOTES_END, releaseDate, whatsNew } from "./updateNotes";

describe("an update's notes", () => {
  it("show what changed, not the download page", () => {
    const body = `- Several domains per server\n- Bare-domain websites\n\n${NOTES_END}\nDownload the installer for your operating system below.\n\nDocker Desktop (with Docker Compose) is required. Commitarium will guide you through setup on first launch.`;
    expect(whatsNew(body)).toBe("- Several domains per server\n- Bare-domain websites");
  });
  it("are empty for a release with only the download page's text", () => {
    expect(
      whatsNew(
        "Download the installer for your operating system below.\n\nDocker Desktop (with Docker Compose) is required. Commitarium will guide you through setup on first launch.",
      ),
    ).toBe("");
    expect(whatsNew(undefined)).toBe("");
  });
});

describe("a release's date", () => {
  it("reads the updater's format", () => {
    expect(releaseDate("2026-11-07 17:55:26.439 +00:00:00")).toBe(
      new Date(2026, 10, 7).toLocaleDateString(),
    );
    expect(releaseDate("not a date")).toBe("");
  });
});
