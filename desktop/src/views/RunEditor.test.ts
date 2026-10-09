import { describe, expect, it } from "vitest";
import { runDraftComplete, runDraftIssue, runFromDraft } from "./RunEditor";

describe("preview open target", () => {
  it("allows the target to remain unset", () => {
    expect(runDraftIssue({ service: "", port: "" })).toBeNull();
    expect(runFromDraft({ service: " ", port: "" })).toBeUndefined();
  });

  it("accepts a compose service and port", () => {
    const draft = { service: "web", port: "5173" };
    expect(runDraftComplete(draft)).toBe(true);
    expect(runFromDraft(draft)).toEqual({ open: { service: "web", port: 5173 } });
  });

  it("requires both halves", () => {
    expect(runDraftIssue({ service: "web", port: "" })).toBe(
      "Add the container port “web” listens on.",
    );
    expect(runDraftIssue({ service: "", port: "5173" })).toBe("Name the compose service to open.");
  });

  it("rejects invalid service names and ports", () => {
    expect(runDraftIssue({ service: "my web", port: "5173" })).toBe(
      "Use the service name exactly as it appears in the compose file.",
    );
    expect(runDraftIssue({ service: "web", port: "0" })).toBe("Use a port from 1 to 65535.");
    expect(runDraftIssue({ service: "web", port: "80a" })).toBe("Use a port from 1 to 65535.");
  });
});
