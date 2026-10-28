import { describe, expect, it } from "vitest";
import { elapsedLabel, stepLabel } from "./AssistantActivity";

describe("the assistant's activity line", () => {
  const reading = { label: "Reading a file", detail: "Looking at src/server/auth.ts" };

  it("says the step in plain words, without paths", () => {
    expect(stepLabel(reading, false)).toBe("Reading a file");
  });
  it("shows what it looked at in Developer mode", () => {
    expect(stepLabel(reading, true)).toBe("Looking at src/server/auth.ts");
    expect(stepLabel({ label: "Writing its reply" }, true)).toBe("Writing its reply");
  });
  it("says when a turn runs again", () => {
    expect(stepLabel({ label: "Fixing its reply", retry: 1 }, false)).toBe(
      "Fixing its reply · trying again",
    );
  });
  it("is empty without a step", () => {
    expect(stepLabel(undefined, true)).toBe("");
  });
  it("counts how long the turn has run", () => {
    const now = Date.parse("2026-10-28T12:03:05Z");
    expect(elapsedLabel("2026-10-28T12:02:53Z", now)).toBe("12 s");
    expect(elapsedLabel("2026-10-28T12:00:00Z", now)).toBe("3:05");
    expect(elapsedLabel(undefined, now)).toBe("");
    expect(elapsedLabel("2026-10-28T12:04:00Z", now)).toBe("0 s");
  });
});
