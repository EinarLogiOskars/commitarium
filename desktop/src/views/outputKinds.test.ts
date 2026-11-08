import { describe, expect, it } from "vitest";
import { csvTable, outputKind, parseCsv, printPage, printable } from "./outputKinds";

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
