import { describe, expect, it } from "vitest";
import type { Conversation } from "../api/assistant";
import { activityLine, elapsedLabel, liveLabel, stepDetail } from "./AssistantActivity";

describe("the assistant's activity line", () => {
  const reading = { label: "Reading a file", detail: "Looking at src/server/auth.ts" };

  it("says what she is doing after her name", () => {
    expect(activityLine(reading)).toBe("Mayka is reading a file");
    expect(activityLine({ label: "Writing the reply" })).toBe("Mayka is writing the reply");
    expect(activityLine(undefined)).toBe("Mayka is thinking…");
  });
  it("shows what it looked at only in Developer mode", () => {
    expect(stepDetail(reading, true)).toBe("Looking at src/server/auth.ts");
    expect(stepDetail(reading, false)).toBe("");
    expect(stepDetail({ label: "Writing the reply" }, true)).toBe("");
  });
  it("says when a turn runs again", () => {
    expect(activityLine({ label: "Fixing the reply", retry: 1 })).toBe(
      "Mayka is fixing the reply · trying again",
    );
  });
  it("counts how long the turn has run", () => {
    const now = Date.parse("2026-10-28T12:03:05Z");
    expect(elapsedLabel("2026-10-28T12:02:53Z", now)).toBe("12 s");
    expect(elapsedLabel("2026-10-28T12:00:00Z", now)).toBe("3:05");
    expect(elapsedLabel(undefined, now)).toBe("");
    expect(elapsedLabel("2026-10-28T12:04:00Z", now)).toBe("0 s");
  });
});

describe("the chat's live line", () => {
  const chat = (fields: Partial<Conversation>) => fields as Conversation;
  const goal = {
    title: "Notes",
    level: 1,
    status: "active" as const,
    started_at: "",
    items: [
      { id: "1", label: "", title: "a", done: true },
      { id: "2", label: "", title: "b", done: false },
    ],
  };

  it("says what the launch, the work orders or autopilot are doing", () => {
    expect(liveLabel(chat({ launch: { stage: "vision", started_at: "" } }), [])).toBe(
      "Writing the app's vision…",
    );
    expect(liveLabel(chat({}), ["Editor"])).toBe("Waiting on “Editor”");
    expect(liveLabel(chat({ goal }), ["Editor", "Search", "Tags"])).toBe(
      "Waiting on work orders · 3 active · autopilot 1 of 2 done",
    );
    expect(liveLabel(chat({ goal }), [])).toBe(
      "Autopilot is picking the next work order · autopilot 1 of 2 done",
    );
    expect(liveLabel(chat({}), [])).toBe("");
  });
});
