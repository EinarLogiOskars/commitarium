import { describe, expect, it } from "vitest";
import { describeServer } from "./serverFacts";
import type { PublishingOverview } from "../ipc";

const base: PublishingOverview = {
  domain: "example.com",
  server_address: "203.0.113.7",
  apps: [],
  sign_in: null,
  authentik: {
    installed: false,
    running: false,
    admin_url: null,
    admin_user: "akadmin",
    memory_total_bytes: 8 * 1024 ** 3,
    memory_needed_gb: 3,
  },
  apps_page: null,
  reach: "internet",
  tailscale: null,
  records: "wildcard",
  placement: "cloud",
};

describe("describeServer", () => {
  it("says what Mayka would otherwise ask", () => {
    expect(describeServer(base)).toBe(
      [
        "Where the owner said it runs: in the cloud",
        "Homelab: off",
        "Domain: example.com, DNS as one wildcard record",
        "Public IP address: 203.0.113.7",
        "Memory: 8.0 GB",
        "Company sign-in: not connected",
        "Apps page: off",
        "Deployed apps: none",
      ].join("\n"),
    );
  });

  it("names Homelab's state instead of a domain", () => {
    const facts = describeServer({
      ...base,
      reach: "private",
      placement: "home",
      tailscale: {
        state: "needs_login",
        login_url: null,
        name: null,
        address: null,
        own: false,
      } as PublishingOverview["tailscale"],
    });
    expect(facts).toContain("Homelab: on (Tailscale needs login)");
    expect(facts).not.toContain("Domain:");
  });
});
