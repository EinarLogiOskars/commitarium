import { describe, expect, it } from "vitest";
import { railHidden } from "./useRail";

describe("railHidden", () => {
  it("follows the user's choice in a wide window", () => {
    expect(railHidden(false, false, false)).toBe(false);
    expect(railHidden(true, false, false)).toBe(true);
  });

  it("folds in a narrow window unless opened anyway", () => {
    expect(railHidden(false, true, false)).toBe(true);
    expect(railHidden(false, true, true)).toBe(false);
  });
});
