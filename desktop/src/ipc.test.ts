import { describe, expect, it } from "vitest";
import { withDefaults, type PublishingOverview } from "./ipc";

const provider = {
  issuer: "https://auth.example.com/application/o/commitarium/",
  client_id: "abc",
  scopes: "openid email profile",
  groups_claim: "groups",
  redirect_url: "https://sign-in.example.com/oauth2/callback",
  sign_out_url: "https://sign-in.example.com/sign-out",
};

describe("withDefaults", () => {
  it("reads a server from before several domains as its one domain", () => {
    const found = withDefaults({
      domain: "example.com",
      records: "each_name",
      sign_in: provider,
      apps_page: { name: "apps", access: "company", url: "https://apps.example.com" },
      authentik: {
        installed: true,
        running: true,
        admin_url: null,
        admin_user: "akadmin",
        memory_total_bytes: null,
        memory_needed_gb: 3,
      },
      apps: [
        {
          project_id: "p1",
          name: "notes",
          access: "public",
          url: "https://notes.example.com",
          live: true,
          groups: [],
          listed: true,
          record_problem: null,
        } as unknown as PublishingOverview["apps"][number],
      ],
    });
    expect(found.domains).toEqual([
      {
        name: "example.com",
        records: "each_name",
        sign_in: provider,
        authentik: true,
        apps_page: { name: "apps", access: "company", url: "https://apps.example.com" },
      },
    ]);
    expect(found.apps[0]).toMatchObject({
      domain: "example.com",
      urls: ["https://notes.example.com"],
      aliases: [],
      www: false,
    });
  });

  it("has no domains on a server without one", () => {
    expect(withDefaults({ domain: null }).domains).toEqual([]);
  });

  it("keeps what a newer server says", () => {
    const domains = [
      {
        name: "a.is",
        records: "wildcard" as const,
        sign_in: null,
        authentik: false,
        apps_page: null,
      },
      {
        name: "b.com",
        records: "wildcard" as const,
        sign_in: null,
        authentik: false,
        apps_page: null,
      },
    ];
    expect(withDefaults({ domain: "a.is", domains }).domains).toBe(domains);
  });
});
