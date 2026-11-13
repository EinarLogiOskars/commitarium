import { describe, expect, it } from "vitest";
import type { Run } from "../api/types";
import { stopped } from "./MergeView";

describe("the merge stage's Continue", () => {
  const run = (fields: Partial<Run>) => ({ status: "waiting_for_user", ...fields }) as Run;
  it("shows for a run paused or blocked at the merge stage", () => {
    expect(stopped(run({ paused: true, wait_kind: "paused" }))).toBe(true);
    expect(stopped(run({ wait_kind: "blocker" }))).toBe(true);
  });
  it("stays away from the plain merge gate and from a running run", () => {
    expect(stopped(run({ wait_kind: "merge_gate" }))).toBe(false);
    expect(stopped(run({ status: "running" }))).toBe(false);
  });
});
