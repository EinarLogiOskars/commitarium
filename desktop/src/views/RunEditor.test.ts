import { describe, expect, it } from "vitest";
import { runDraftComplete, runDraftIssue, type RunDraft } from "./RunEditor";

const valid: RunDraft = {
  setup: "npm ci",
  processes: [{ name: "web", command: "npm run dev -- --host 0.0.0.0", port: "5173", open: true }],
};

describe("manual preview setup validation", () => {
  it("allows preview setup to remain optional", () => {
    expect(runDraftIssue({ setup: "", processes: [] })).toBeNull();
    expect(runDraftComplete({ setup: "", processes: [] })).toBe(true);
  });

  it("requires a process when setup commands have been entered", () => {
    expect(runDraftIssue({ setup: "npm ci", processes: [] })).toBe(
      "Add at least one process to make the project previewable.",
    );
  });

  it("accepts a complete preview setup", () => {
    expect(runDraftIssue(valid)).toBeNull();
    expect(runDraftComplete(valid)).toBe(true);
  });

  it("rejects invalid process names and ports", () => {
    expect(
      runDraftIssue({
        ...valid,
        processes: [{ ...valid.processes[0], name: "Web app" }],
      }),
    ).toContain("Process names start");
    expect(
      runDraftIssue({
        ...valid,
        processes: [{ ...valid.processes[0], port: "70000" }],
      }),
    ).toContain("1 to 65535");
  });

  it("requires unique names and ports and only one browser process", () => {
    const duplicate = { ...valid.processes[0], command: "npm run api" };
    expect(runDraftIssue({ ...valid, processes: [valid.processes[0], duplicate] })).toContain(
      "used more than once",
    );

    expect(
      runDraftIssue({
        ...valid,
        processes: [
          valid.processes[0],
          { name: "api", command: "npm run api", port: "5174", open: true },
        ],
      }),
    ).toBe("Choose only one process to open in the browser.");
  });

  it("requires a port for the process opened in the browser", () => {
    expect(
      runDraftIssue({
        ...valid,
        processes: [{ ...valid.processes[0], port: "" }],
      }),
    ).toBe("Add a port for “web” so it can open in the browser.");
  });
});
