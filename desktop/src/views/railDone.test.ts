import { describe, expect, it } from "vitest";
import { DONE_SHOWN, doneShown } from "./railDone";
import type { Feature } from "../api/types";

const done = (index: number): Feature =>
  ({
    id: `fea_${index}`,
    title: `Done ${index}`,
    state: "completed",
    updated_at: new Date(Date.UTC(2026, 9, 14, index)).toISOString(),
  }) as Feature;

describe("the rail's Done group", () => {
  const items = [1, 2, 3, 4, 5, 6, 7].map(done);

  it("shows the most recent few, newest first, until expanded", () => {
    const { shown, hidden } = doneShown(items, false, null);
    expect(shown.map((f) => f.id)).toEqual(["fea_7", "fea_6", "fea_5", "fea_4"]);
    expect(hidden).toBe(items.length - DONE_SHOWN);
    expect(doneShown(items, true, null)).toEqual({
      shown: [...items].reverse(),
      hidden: 0,
    });
  });

  it("keeps the one on screen listed", () => {
    const { shown, hidden } = doneShown(items, false, "fea_1");
    expect(shown.map((f) => f.id)).toEqual(["fea_7", "fea_6", "fea_5", "fea_4", "fea_1"]);
    expect(hidden).toBe(2);
  });

  it("hides nothing when there are only a few", () => {
    expect(doneShown(items.slice(0, DONE_SHOWN), false, null).hidden).toBe(0);
  });
});
