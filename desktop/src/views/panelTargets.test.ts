import { describe, expect, it } from "vitest";
import type { HelperFile, OutputFile } from "../api/outputs";
import {
  FOLD_OVER,
  autoOpenTarget,
  foldOutputs,
  imagesLabel,
  outputRows,
  liveFor,
  liveHelper,
  panelTitle,
  shownKey,
  storedFor,
  towardPdf,
  versionLabel,
} from "./panelTargets";

const file = (over: Partial<OutputFile> = {}): OutputFile => ({
  name: "deck.pdf",
  size: 10,
  made_at: "2026-11-09T10:00:00Z",
  helper: "tsk_1",
  version: 2,
  ...over,
});
const live = (name: string, made_at = "2026-11-09T10:05:00Z"): HelperFile => ({
  name,
  size: 12,
  made_at,
});

describe("storedFor", () => {
  it("finds an output by name", () => {
    expect(storedFor({ name: "deck.pdf" }, [file()])?.version).toBe(2);
  });
  it("finds a helper's file once it is handed back, by its own name", () => {
    const renamed = file({ name: "deck-2.pdf", source: "deck.pdf" });
    expect(storedFor({ helper: "tsk_1", live: "deck.pdf" }, [renamed])?.name).toBe("deck-2.pdf");
    expect(storedFor({ helper: "tsk_2", live: "deck.pdf" }, [renamed])).toBeNull();
  });
});

describe("liveFor", () => {
  it("is the helper's file of the output's own name", () => {
    const renamed = file({ name: "deck-2.pdf", source: "deck.pdf" });
    expect(liveFor({ name: "deck-2.pdf" }, renamed, [live("x.md"), live("deck.pdf")])?.name).toBe(
      "deck.pdf",
    );
  });
  it("is the target's file before anything is stored", () => {
    expect(liveFor({ helper: "tsk_1", live: "cv.html" }, null, [live("cv.html")])?.name).toBe(
      "cv.html",
    );
    expect(liveFor({ helper: "tsk_1", live: "cv.html" }, null, [])).toBeNull();
  });
});

describe("liveHelper", () => {
  it("follows the helper only while it is at work", () => {
    expect(liveHelper({ name: "deck.pdf" }, file(), new Set(["tsk_1"]))).toBe("tsk_1");
    expect(liveHelper({ name: "deck.pdf" }, file(), new Set())).toBeNull();
    expect(liveHelper({ helper: "tsk_3", live: "a.md" }, null, new Set(["tsk_3"]))).toBe("tsk_3");
  });
});

describe("shownKey", () => {
  it("changes when the file, the version or the live file changes", () => {
    const keys = new Set([
      shownKey(file(), null, null),
      shownKey(file({ version: 3, made_at: "2026-11-09T11:00:00Z" }), null, null),
      shownKey(file(), 1, null),
      shownKey(file(), null, live("deck.pdf")),
      shownKey(file(), null, live("deck.pdf", "2026-11-09T10:06:00Z")),
      shownKey(null, null, null),
    ]);
    expect(keys.size).toBe(6);
  });
});

describe("autoOpenTarget", () => {
  it("opens on the first helper's first file, once", () => {
    const files = new Map([
      ["tsk_1", []],
      ["tsk_2", [live("plan.md"), live("data.csv")]],
    ]);
    expect(autoOpenTarget(["tsk_1", "tsk_2"], new Set(), files)).toEqual({
      helper: "tsk_2",
      live: "plan.md",
    });
    expect(autoOpenTarget(["tsk_1", "tsk_2"], new Set(["tsk_2"]), files)).toBeNull();
  });
});

describe("labels", () => {
  it("names versions and the panel", () => {
    expect(versionLabel({ number: 3, size: 1, made_at: "" }, 3)).toBe("v3 · latest");
    expect(versionLabel({ number: 1, size: 1, made_at: "" }, 3)).toBe("v1");
    expect(panelTitle({ helper: "tsk_1", live: "cv.html" }, null)).toBe("cv.html");
    expect(panelTitle({ name: "a.md" }, file({ name: "a.md" }))).toBe("a.md");
  });
});

describe("folding many files", () => {
  const shots = Array.from({ length: 20 }, (_, i) => `${String(i + 1).padStart(2, "0")}-step.png`);
  it("keeps a few files as they are", () => {
    expect(foldOutputs(["a.md", "b.png", "c.png"])).toEqual({
      documents: ["a.md", "b.png", "c.png"],
      images: [],
    });
  });
  it("puts the report first and folds the screenshots, in order", () => {
    const folded = foldOutputs([...shots].reverse().concat("test-report.md"));
    expect(folded.documents).toEqual(["test-report.md"]);
    expect(folded.images[0]).toBe("01-step.png");
    expect(folded.images).toHaveLength(20);
    expect(imagesLabel(folded.images)).toBe("20 screenshots");
    expect(imagesLabel(["logo.png", "logo-512.png"])).toBe("2 images");
  });
  it("groups a helper's run of many files into one row of the card", () => {
    const run = shots.map((name) => file({ name, helper: "tsk_test", title: "Checkout test" }));
    const rows = outputRows([file({ name: "plan.md", helper: "tsk_2" }), ...run]);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({ kind: "file" });
    expect(rows[1]).toMatchObject({ kind: "run", title: "Checkout test" });
    expect(run.length).toBeGreaterThan(FOLD_OVER);
  });
});

describe("a deck's page and its PDF", () => {
  it("folds the page away when its PDF is there", () => {
    expect(foldOutputs(["deck.html", "deck.pdf", "notes.html"]).documents).toEqual([
      "deck.pdf",
      "notes.html",
    ]);
  });

  it("opens the PDF instead of the page once there is one", () => {
    expect(towardPdf({ name: "deck.html" }, ["deck.html", "deck.pdf"])).toEqual({
      name: "deck.pdf",
      live: undefined,
    });
    expect(towardPdf({ helper: "h1", live: "deck.html" }, ["deck.html"])).toEqual({
      helper: "h1",
      live: "deck.html",
    });
  });

  it("opens by itself on the PDF when the helper has made both", () => {
    const files = new Map([
      [
        "h1",
        [
          { name: "deck.html", size: 1, made_at: "" },
          { name: "deck.pdf", size: 1, made_at: "" },
        ],
      ],
    ]);
    expect(autoOpenTarget(["h1"], new Set(), files)).toEqual({ helper: "h1", live: "deck.pdf" });
  });
});
