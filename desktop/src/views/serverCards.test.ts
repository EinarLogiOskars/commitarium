import { describe, expect, it } from "vitest";
import { deployAddress, findApp, serverCardQuestion } from "./serverCards";
import type { PublishedApp } from "../ipc";

const app = (project_id: string, domain: string, name: string, more: Partial<PublishedApp> = {}) =>
  ({
    project_id,
    domain,
    name,
    access: "public",
    url: "",
    urls: [],
    www: true,
    aliases: [],
    live: true,
    groups: [],
    listed: true,
    record_problem: null,
    ...more,
  }) as PublishedApp;

describe("serverCardQuestion", () => {
  it("asks each card's change in plain words", () => {
    expect(serverCardQuestion({ type: "add_domain", domain: "commitarium.com", how: "" })).toEqual([
      "Add commitarium.com to your server, with one wildcard DNS record?",
      "Add domain",
    ]);
    expect(serverCardQuestion({ type: "install_authentik", domain: "b.com" }, true)).toEqual([
      "Let b.com sign people in with this server's authentik?",
      "Connect",
    ]);
    expect(serverCardQuestion({ type: "install_authentik" })[1]).toBe("Set up authentik");
    expect(
      serverCardQuestion({
        type: "set_app_access",
        how: "company",
        app: "notes.a.is",
        groups: ["sales"],
      })[0],
    ).toBe("Make notes.a.is open with company sign-in, for sales?");
    expect(serverCardQuestion({ type: "set_app_access", how: "private" })[0]).toBe(
      "Make this app need its password?",
    );
    expect(serverCardQuestion({ type: "set_homelab", how: "off" })).toEqual([
      "Turn off Homelab?",
      "Turn off",
    ]);
  });
});

describe("findApp", () => {
  const apps = [
    app("p1", "einarlogi.is", "", { aliases: [{ domain: "einarlogi.com", name: "", url: "" }] }),
    app("p2", "einarlogi.is", "notes"),
  ];

  it("finds an app by any of its addresses, or the project's own", () => {
    expect(findApp(apps, "notes.einarlogi.is", null)?.project_id).toBe("p2");
    expect(findApp(apps, "https://einarlogi.com/", null)?.project_id).toBe("p1");
    expect(findApp(apps, "", "p2")?.project_id).toBe("p2");
    expect(findApp(apps, "", null)).toBeUndefined();
    expect(findApp(apps, "shop.einarlogi.is", null)).toBeUndefined();
  });
});

describe("deployAddress", () => {
  it("names the main address and the extra ones", () => {
    expect(
      deployAddress({
        domain: "einarlogi.is",
        name: "",
        aliases: [{ domain: "einarlogi.com", name: "" }],
      }),
    ).toBe("einarlogi.is and einarlogi.com");
  });
});
