import { describe, expect, it } from "vitest";
import { needsApproval, tierLabel } from "./modelGuide";

describe("the model guide", () => {
  it("asks before Premium and unrated models", () => {
    expect(needsApproval({ id: "fable", tier: "premium", source: "official" })).toBe(true);
    expect(needsApproval({ id: "new", source: "unrated" })).toBe(true);
    expect(needsApproval(undefined)).toBe(true);
    expect(needsApproval({ id: "sonnet", tier: "strong", source: "official" })).toBe(false);
    expect(needsApproval({ id: "opus", tier: "frontier", source: "user" })).toBe(false);
  });

  it("names tiers in the user's words", () => {
    expect(tierLabel("frontier")).toBe("Frontier");
    expect(tierLabel(undefined)).toBe("Not rated");
  });
});
