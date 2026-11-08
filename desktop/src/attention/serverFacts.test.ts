import { describe, expect, it } from "vitest";
import { describeServer } from "./serverFacts";
import type { PublishedApp, PublishingOverview } from "../ipc";

const base: PublishingOverview = {
  domains: [
    { name: "example.com", records: "wildcard", sign_in: null, authentik: false, apps_page: null },
  ],
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

const app = (more: Partial<PublishedApp>): PublishedApp => ({
  project_id: "p1",
  domain: "example.com",
  name: "notes",
  access: "public",
  url: "https://notes.example.com",
  urls: ["https://notes.example.com"],
  www: true,
  aliases: [],
  live: true,
  groups: [],
  listed: true,
  record_problem: null,
  ...more,
});

describe("describeServer", () => {
  it("says what Mayka would otherwise ask", () => {
    expect(describeServer(base)).toBe(
      [
        "Where the owner said it runs: in the cloud",
        "Homelab: off",
        "Domain: example.com, DNS as one wildcard record, not checked yet (no app there); company sign-in not connected; apps page off",
        "Public IP address: 203.0.113.7",
        "Memory: 8.0 GB",
        "Deployed apps: none",
      ].join("\n"),
    );
  });

  it("describes every domain and each app's full addresses", () => {
    const provider = {
      issuer: "https://auth.einarlogi.is/application/o/commitarium/",
      client_id: "x",
      scopes: "",
      groups_claim: "",
      redirect_url: null,
      sign_out_url: null,
    };
    const facts = describeServer(
      {
        ...base,
        domains: [
          {
            name: "einarlogi.is",
            records: "wildcard",
            sign_in: provider,
            authentik: true,
            apps_page: { name: "apps", access: "company", url: "https://apps.einarlogi.is" },
          },
          {
            name: "einarlogi.com",
            records: "each_name",
            sign_in: null,
            authentik: false,
            apps_page: null,
          },
        ],
        authentik: {
          ...base.authentik,
          installed: true,
          running: true,
          admin_url: "https://auth.einarlogi.is/if/admin/",
        },
        apps: [
          app({
            domain: "einarlogi.is",
            name: "",
            url: "https://einarlogi.is",
            urls: ["https://einarlogi.is", "https://einarlogi.com"],
            aliases: [{ domain: "einarlogi.com", name: "", url: "https://einarlogi.com" }],
          }),
          app({
            project_id: "p2",
            domain: "einarlogi.is",
            name: "notes",
            access: "company",
            groups: ["family"],
            url: "https://notes.einarlogi.is",
            urls: ["https://notes.einarlogi.is"],
            live: false,
            record_problem: "notes.einarlogi.is does not point here",
          }),
        ],
      },
      "p1",
    );
    expect(facts).toContain(
      "Domain: einarlogi.is, DNS as one wildcard record, a record is missing (see the apps); company sign-in authentik on this server; apps page on, behind company sign-in, at apps.einarlogi.is",
    );
    expect(facts).toContain(
      "Domain: einarlogi.com, DNS as a record for each address, not checked yet (no app there); company sign-in not connected; apps page off",
    );
    expect(facts).toContain("authentik: set up on this server at auth.einarlogi.is, running");
    expect(facts).toContain(
      "Deployed apps: einarlogi.is + einarlogi.com (and www) (open to anyone, this project's app), notes.einarlogi.is (behind company sign-in, groups family, stopped, DNS: notes.einarlogi.is does not point here)",
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
    expect(facts).toContain("Apps page: off");
  });
});
