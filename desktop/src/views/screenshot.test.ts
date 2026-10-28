import { describe, expect, it } from "vitest";
import { captureScreenshot } from "./screenshot";
import { attachmentName } from "../api/attachments";

describe("taking a screenshot of the app", () => {
  it("hides the assistant while the window is captured, then shows it again", async () => {
    const steps: string[] = [];
    const file = await captureScreenshot({
      hide: (hidden) => steps.push(hidden ? "hide" : "show"),
      painted: async () => void steps.push("painted"),
      snapshot: async () => {
        steps.push("snapshot");
        return new Blob(["png"], { type: "image/png" });
      },
    });
    expect(steps).toEqual(["hide", "painted", "snapshot", "show"]);
    expect(file.type).toBe("image/png");
    // It attaches as a pasted screenshot does.
    expect(attachmentName(file, new Date(2026, 9, 28, 16, 12))).toBe(
      "screenshot-2026-10-28-1612.png",
    );
  });
  it("shows the assistant again when the capture fails", async () => {
    const steps: string[] = [];
    await expect(
      captureScreenshot({
        hide: (hidden) => steps.push(hidden ? "hide" : "show"),
        painted: async () => undefined,
        snapshot: () => Promise.reject(new Error("no window")),
      }),
    ).rejects.toThrow("no window");
    expect(steps).toEqual(["hide", "show"]);
  });
});
