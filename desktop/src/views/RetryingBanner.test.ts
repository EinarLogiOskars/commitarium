import { describe, expect, it } from "vitest";
import { retryLabel } from "./RetryingBanner";

describe("retry label", () => {
  const now = Date.parse("2026-10-13T12:00:00Z");
  it("counts down to the retry", () => {
    expect(retryLabel("2026-10-13T12:00:30Z", now)).toBe("in 30 s");
    expect(retryLabel("2026-10-13T12:02:00Z", now)).toBe("in 2 min");
    expect(retryLabel("2026-10-13T12:04:10Z", now)).toBe("in 5 min");
  });
  it("says now once the retry is due", () => {
    expect(retryLabel("2026-10-13T11:59:00Z", now)).toBe("now");
    expect(retryLabel("not a time", now)).toBe("now");
  });
});
