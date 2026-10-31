import { describe, expect, it } from "vitest";
import type { PreviewState, PreviewStatus } from "../ipc";
import { isStopped } from "./usePreview";

const status = (state: PreviewState): PreviewStatus => ({
  projectId: "p1",
  target: { kind: "canonical" },
  commitId: "c1",
  state,
  urls: [],
  error: null,
});

describe("isStopped", () => {
  it("is true for a preview stopped while nothing is starting it", () => {
    expect(isStopped(status("stopped"), false, null)).toBe(true);
  });

  it("is false while a start is on its way, as when the window first opens", () => {
    expect(isStopped(status("stopped"), true, null)).toBe(false);
    expect(isStopped(null, false, null)).toBe(false);
  });

  it("leaves a start that threw to the failure state", () => {
    expect(isStopped(status("stopped"), false, "no stack")).toBe(false);
  });

  it("is false for a preview in any other state", () => {
    for (const state of ["starting", "running", "failed"] as const) {
      expect(isStopped(status(state), false, null)).toBe(false);
    }
  });
});
