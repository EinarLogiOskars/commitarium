import { describe, expect, it } from "vitest";
import {
  csvTable,
  imageType,
  openable,
  outputKind,
  parseCsv,
  presentKey,
  printPage,
  printable,
  sizeLabel,
} from "./outputKinds";

describe("outputs", () => {
  it("knows each kind by its extension", () => {
    expect(outputKind("report.md")).toBe("markdown");
    expect(outputKind("deck.HTML")).toBe("html");
    expect(outputKind("chart.svg")).toBe("image");
    expect(outputKind("sales.csv")).toBe("csv");
    expect(outputKind("notes.txt")).toBe("text");
    expect(outputKind("brief.pdf")).toBe("pdf");
    expect(outputKind("archive.zip")).toBe("other");
    expect(printable("pdf")).toBe(false);
    expect(printable("markdown")).toBe(true);
    expect(printable("html")).toBe(true);
    expect(printable("image")).toBe(false);
    expect(printable("csv")).toBe(false);
  });

  it("shows an SVG as an image, and opens only documents elsewhere", () => {
    expect(imageType("logo.SVG")).toBe("image/svg+xml");
    expect(imageType("photo.jpg")).toBe("image/jpeg");
    expect(openable("plan.docx")).toBe(true);
    expect(openable("Deck.PPTX")).toBe(true);
    expect(openable("run.command")).toBe(false);
    expect(openable("book.xlsm")).toBe(false);
    expect(openable("README")).toBe(false);
    expect(sizeLabel(2048)).toBe("2 KB");
    expect(sizeLabel(3.5 * 1024 * 1024)).toBe("3.5 MB");
  });

  it("moves through a presentation by key", () => {
    expect(presentKey("ArrowRight", 1, 3)).toBe(2);
    expect(presentKey(" ", 3, 3)).toBe(3);
    expect(presentKey("ArrowLeft", 1, 3)).toBe(1);
    expect(presentKey("End", 1, 3)).toBe(3);
    expect(presentKey("Escape", 2, 3)).toBe("leave");
    expect(presentKey("a", 2, 3)).toBeNull();
  });

  it("reads quoted CSV fields", () => {
    expect(parseCsv('name,note\r\n"Smith, J","said ""hi"""\nA,\n')).toEqual([
      ["name", "note"],
      ["Smith, J", 'said "hi"'],
      ["A", ""],
    ]);
  });

  it("escapes what it puts in a page", () => {
    const table = csvTable([["<b>"], ["<script>x</script>"]]);
    expect(table).not.toContain("<script>");
    expect(table).toContain("&lt;b&gt;");
    expect(printPage("A <b>", "<p>x</p>")).toContain("<title>A &lt;b&gt;</title>");
  });
});
