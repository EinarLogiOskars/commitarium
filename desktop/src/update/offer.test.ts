import { describe, expect, it } from "vitest";
import { shouldOffer } from "./offer";

describe("shouldOffer", () => {
  it("offers a new version once", () => {
    expect(shouldOffer("available", "0.9.5", 0, null)).toBe(true);
    expect(shouldOffer("available", "0.9.5", 0, "0.9.4")).toBe(true);
    expect(shouldOffer("available", "0.9.5", 0, "0.9.5")).toBe(false);
  });

  it("waits while agents work", () => {
    expect(shouldOffer("available", "0.9.5", 2, null)).toBe(false);
  });

  it("offers nothing without an update", () => {
    expect(shouldOffer("up_to_date", undefined, 0, null)).toBe(false);
  });
});
