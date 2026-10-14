import { describe, expect, it } from "vitest";
import { failingFirst, plainFailure, serviceState } from "./previewEvidence";

const web = { service: "web", state: "running", exit_code: 0 };
const api = { service: "api", state: "exited", exit_code: 1, logs: "ModuleNotFoundError\n" };

describe("preview evidence", () => {
  it("names the container that stopped and its exit code", () => {
    expect(plainFailure([web, api])).toBe(
      "The “api” part of the app stops right after it starts (exit code 1).",
    );
    expect(failingFirst([web, api]).map((s) => s.service)).toEqual(["api", "web"]);
  });

  it("tells crashing, unhealthy, and finished services apart", () => {
    const restarting = { service: "web", state: "restarting", exit_code: 137 };
    const unhealthy = { service: "db", state: "running", exit_code: 0, health: "unhealthy" };
    expect(plainFailure([restarting])).toBe(
      "The “web” part of the app keeps crashing and restarting.",
    );
    expect(plainFailure([unhealthy])).toBe(
      "The “db” part of the app starts but reports that it is not working.",
    );
    expect(serviceState({ service: "migrate", state: "exited", exit_code: 0 })).toBe("finished");
    expect(serviceState(restarting)).toBe("keeps restarting (exit code 137)");
    expect(plainFailure([web])).toBeNull();
    expect(plainFailure(undefined)).toBeNull();
  });
});
