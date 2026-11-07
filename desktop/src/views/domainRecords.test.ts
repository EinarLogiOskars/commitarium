import { describe, expect, it } from "vitest";
import { appsOn, authentikHome, recordsFor } from "./domainRecords";
import type { DomainShown, PublishedApp } from "../ipc";

const domain = (name: string, records: DomainShown["records"] = "wildcard"): DomainShown => ({
  name,
  records,
  sign_in: null,
  authentik: false,
  apps_page: null,
});

const app = (project_id: string, at: string, name: string, more: Partial<PublishedApp> = {}) =>
  ({
    project_id,
    domain: at,
    name,
    access: "public",
    url: `https://${name ? name + "." : ""}${at}`,
    urls: [],
    www: true,
    aliases: [],
    live: true,
    groups: [],
    listed: true,
    record_problem: null,
    ...more,
  }) as PublishedApp;

describe("recordsFor", () => {
  it("asks for @ and www beside a wildcard once a website is at the bare domain", () => {
    expect(recordsFor(domain("a.is"), [app("p1", "a.is", "notes")])).toEqual(["*"]);
    expect(recordsFor(domain("a.is"), [app("p1", "a.is", "")])).toEqual(["*", "@", "www"]);
    expect(recordsFor(domain("a.is"), [app("p1", "a.is", "", { www: false })])).toEqual(["*", "@"]);
  });

  it("counts an extra address on the domain", () => {
    const site = app("p1", "a.is", "", { aliases: [{ domain: "a.com", name: "", url: "" }] });
    expect(recordsFor(domain("a.com"), [site])).toEqual(["*", "@", "www"]);
  });

  it("lists every name in use with a record for each", () => {
    const d = {
      ...domain("a.is", "each_name"),
      apps_page: { name: "apps", access: "public" as const, url: "" },
    };
    expect(recordsFor(d, [app("p1", "a.is", "notes"), app("p2", "b.com", "x")], true)).toEqual([
      "apps",
      "auth",
      "notes",
    ]);
  });
});

describe("appsOn", () => {
  it("puts the apps whose main address is there first", () => {
    const site = app("p1", "a.is", "", { aliases: [{ domain: "a.com", name: "", url: "" }] });
    const shop = app("p2", "a.com", "shop");
    expect(appsOn("a.com", [site, shop]).map((a) => a.project_id)).toEqual(["p2", "p1"]);
    expect(appsOn("b.org", [site, shop])).toEqual([]);
  });
});

describe("authentikHome", () => {
  it("reads it from the admin page's address", () => {
    const domains = [domain("a.is"), domain("b.com")];
    expect(authentikHome("https://auth.b.com/if/admin/", domains)).toBe("b.com");
    expect(authentikHome(null, domains)).toBeNull();
  });
});
