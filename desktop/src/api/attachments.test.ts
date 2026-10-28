import { describe, expect, it } from "vitest";
import {
  attachmentName,
  attachmentProblem,
  isImageName,
  splitAttached,
  transferredFiles,
} from "./attachments";

const at = new Date(2026, 9, 28, 16, 12);

describe("an attachment's name", () => {
  it("names a pasted screenshot by when it was pasted", () => {
    expect(attachmentName({ name: "image.png", type: "image/png" }, at)).toBe(
      "screenshot-2026-10-28-1612.png",
    );
    expect(attachmentName({ name: "", type: "image/jpeg" }, at)).toBe(
      "screenshot-2026-10-28-1612.jpg",
    );
  });
  it("keeps a file's own name, made safe for a document", () => {
    expect(
      attachmentName({ name: "Screenshot 2026-10-28 at 16.12.03.png", type: "image/png" }, at),
    ).toBe("Screenshot 2026-10-28 at 16.12.03.png");
    expect(attachmentName({ name: "spec, v2’s final.pdf", type: "application/pdf" }, at)).toBe(
      "spec- v2-s final.pdf",
    );
    expect(attachmentName({ name: "../.env", type: "" }, at)).toBe("env");
    const long = attachmentName({ name: "a".repeat(150) + ".csv", type: "text/csv" }, at);
    expect(long).toHaveLength(100);
    expect(long.endsWith(".csv")).toBe(true);
  });
});

describe("what can be attached", () => {
  it("refuses empty files and files over 5 MB in plain words", () => {
    expect(attachmentProblem({ name: "a.png", size: 0 })).toBe("a.png is empty.");
    expect(attachmentProblem({ name: "big.mov", size: 5 * 1024 * 1024 + 1 })).toBe(
      "big.mov is larger than 5 MB.",
    );
    expect(attachmentProblem({ name: "ok.png", size: 5 * 1024 * 1024 })).toBeNull();
  });
  it("shows images, and only images, as images", () => {
    for (const name of ["a.png", "b.JPG", "c.jpeg", "d.gif", "e.webp"]) {
      expect(isImageName(name)).toBe(true);
    }
    for (const name of ["a.pdf", "b.svg", "png", "c.png.txt"])
      expect(isImageName(name)).toBe(false);
  });
});

describe("files on the clipboard or in a drop", () => {
  const file = (name: string) => new File(["x"], name, { type: "image/png" });
  it("takes a copied screenshot from the clipboard's items", () => {
    const shot = file("image.png");
    const items = [
      { kind: "string", getAsFile: () => null },
      { kind: "file", getAsFile: () => shot },
    ];
    expect(transferredFiles({ items, files: [] })).toEqual([shot]);
  });
  it("falls back to the files, and finds nothing in plain text", () => {
    const dropped = file("photo.png");
    expect(transferredFiles({ files: [dropped] })).toEqual([dropped]);
    expect(
      transferredFiles({ items: [{ kind: "string", getAsFile: () => null }], files: [] }),
    ).toEqual([]);
    expect(transferredFiles(null)).toEqual([]);
  });
});

describe("a sent message's files", () => {
  it("are read from the line naming them, which the transcript leaves out", () => {
    const text =
      "Fix the header\n\nAttached, in your working directory: .commitarium/documents/screenshot-2026-10-28-1612.png, .commitarium/documents/spec (1).pdf";
    expect(splitAttached(text)).toEqual({
      text: "Fix the header",
      names: ["screenshot-2026-10-28-1612.png", "spec (1).pdf"],
    });
  });
  it("leave a message without them as it is", () => {
    expect(splitAttached("Attached is my note.\nThanks")).toEqual({
      text: "Attached is my note.\nThanks",
      names: [],
    });
  });
});
