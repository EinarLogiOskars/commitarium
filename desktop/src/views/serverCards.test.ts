import { describe, expect, it } from "vitest";
import {
  appliesAtOnce,
  deployAddress,
  deployChange,
  findApp,
  serverCardQuestion,
} from "./serverCards";
import type { AssistantAction } from "../api/assistant";
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

const card = (more: Partial<AssistantAction>) =>
  ({ id: "a1", type: "deploy_app", asked: true, ...more }) as AssistantAction;

describe("a deploy that moves an app", () => {
  const hello = app("p1", "t1.einarlogi.is", "");

  it("says it moves, and never applies by itself", () => {
    const moved = card({ domain: "t1.einarlogi.com", name: "hello" });
    expect(deployChange(moved, hello)).toEqual({
      from: "t1.einarlogi.is",
      dropped: ["t1.einarlogi.is"],
    });
    expect(serverCardQuestion(moved, false, hello)[0]).toBe(
      "Deploy the latest version at hello.t1.einarlogi.com? This moves it from t1.einarlogi.is.",
    );
    expect(appliesAtOnce(moved, hello)).toBe(false);
  });

  it("applies an added address at once, keeping the main one", () => {
    const added = card({
      domain: "t1.einarlogi.is",
      name: "",
      aliases: [{ domain: "t1.einarlogi.com", name: "hello" }],
    });
    expect(deployChange(added, hello)).toEqual({ from: null, dropped: [] });
    expect(appliesAtOnce(added, hello)).toBe(true);
  });

  it("names an extra address that would stop working", () => {
    const withAlias = app("p1", "t1.einarlogi.is", "", {
      aliases: [{ domain: "t1.einarlogi.com", name: "hello", url: "" }],
    });
    const dropping = card({ domain: "t1.einarlogi.is", name: "" });
    expect(serverCardQuestion(dropping, false, withAlias)[0]).toBe(
      "Deploy the latest version at t1.einarlogi.is? hello.t1.einarlogi.com stops working.",
    );
    expect(appliesAtOnce(dropping, withAlias)).toBe(false);
  });
});

describe("appliesAtOnce", () => {
  it("applies what the user asked for, but never removing, hiding or Homelab", () => {
    expect(appliesAtOnce(card({ type: "set_app_access", how: "public" }))).toBe(true);
    expect(appliesAtOnce(card({ type: "set_app_access", how: "public", asked: false }))).toBe(
      false,
    );
    expect(appliesAtOnce(card({ type: "set_apps_page", how: "public" }))).toBe(true);
    expect(appliesAtOnce(card({ type: "set_apps_page", how: "off" }))).toBe(false);
    expect(appliesAtOnce(card({ type: "list_on_apps_page", how: "hide" }))).toBe(false);
    expect(appliesAtOnce(card({ type: "set_homelab", how: "on" }))).toBe(false);
    expect(appliesAtOnce(card({ type: "create_work_order" }))).toBe(false);
  });
});
