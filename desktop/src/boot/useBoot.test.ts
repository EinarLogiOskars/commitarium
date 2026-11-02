import { describe, expect, it } from "vitest";
import { coreServicesUp } from "./useBoot";

const service = (name: string, state = "running") => ({ service: name, state }) as never;

describe("coreServicesUp", () => {
  it("needs every core service running", () => {
    const up = [service("coordinator"), service("forgejo"), service("simulated-codex-worker")];
    expect(coreServicesUp(up)).toBe(true);
    expect(coreServicesUp(up.slice(0, 2))).toBe(false);
    expect(coreServicesUp([...up.slice(0, 2), service("simulated-codex-worker", "exited")])).toBe(
      false,
    );
  });
});
