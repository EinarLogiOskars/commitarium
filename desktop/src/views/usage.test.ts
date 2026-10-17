import { describe, expect, it } from "vitest";
import { costParts, formatUSD } from "./usage";

const none = { input_tokens: 0, cached_input_tokens: 0, cache_write_tokens: 0, output_tokens: 0 };

describe("cost", () => {
  it("shows dollars for API keys and tokens for the rest", () => {
    const parts = costParts({
      api_cost_usd: 3.5,
      api_unpriced: none,
      subscription: { ...none, input_tokens: 1_200_000, output_tokens: 3000 },
      unknown: none,
      prices_as_of: "2026-10-17",
    });
    expect(parts.map((p) => p.label)).toEqual(["API keys", "Subscriptions"]);
    expect(parts[0].value).toBe("≈ $3.50");
    expect(parts[1].value).toContain("1.2M in");
  });

  it("formats small amounts plainly", () => {
    expect(formatUSD(0.004)).toBe("< $0.01");
    expect(formatUSD(0)).toBe("$0.00");
  });
});
