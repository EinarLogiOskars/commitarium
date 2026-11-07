import { describe, expect, it } from "vitest";
import { hostOf, placeChanged, slug } from "./DeployCard";
import type { PublishedApp } from "../ipc";

describe("slug", () => {
  it("makes an address name from a project's name", () => {
    expect(slug("Simple To-Do")).toBe("simple-to-do");
    expect(slug("  Mañana en Islandia! ")).toBe("manana-en-islandia");
    expect(slug("¡Vamos, Vikingo!")).toBe("vamos-vikingo");
  });
});

describe("placeChanged", () => {
  const published = {
    project_id: "p1",
    domain: "einarlogi.is",
    name: "",
    access: "public",
    url: "https://einarlogi.is",
    urls: ["https://einarlogi.is", "https://einarlogi.com"],
    www: true,
    aliases: [{ domain: "einarlogi.com", name: "", url: "https://einarlogi.com" }],
    live: true,
    groups: [],
    listed: true,
    record_problem: null,
  } as PublishedApp;
  const same = {
    domain: "einarlogi.is",
    name: "",
    www: true,
    aliases: [{ domain: "einarlogi.com", name: "" }],
  };

  it("sees no change in the same address", () => {
    expect(placeChanged(published, same)).toBe(false);
  });

  it("sees a new domain, name, www or extra address", () => {
    expect(placeChanged(published, { ...same, domain: "commitarium.com" })).toBe(true);
    expect(placeChanged(published, { ...same, name: "Home" })).toBe(true);
    expect(placeChanged(published, { ...same, www: false })).toBe(true);
    expect(placeChanged(published, { ...same, aliases: [] })).toBe(true);
  });

  it("names a bare address by its domain", () => {
    expect(hostOf({ domain: "einarlogi.com", name: "" })).toBe("einarlogi.com");
    expect(hostOf({ domain: "einarlogi.is", name: "notes" })).toBe("notes.einarlogi.is");
  });
});
