import { describe, expect, it } from "vitest";
import type { Project } from "../api/types";
import { later, sidebarProjects } from "./sidebarProjects";

const project = (id: string, created: string) => ({ id, name: id, created_at: created }) as Project;

describe("sidebarProjects", () => {
  const projects = [
    project("a", "2026-01-01T00:00:00Z"),
    project("b", "2026-02-01T00:00:00Z"),
    project("c", "2026-03-01T00:00:00Z"),
    project("d", "2026-04-01T00:00:00Z"),
  ];

  it("lists the most recently worked on first, up to the limit", () => {
    const { shown, hidden } = sidebarProjects(
      projects,
      { a: "2026-10-01T00:00:00Z" },
      new Set(),
      2,
    );
    expect(shown.map((p) => p.id)).toEqual(["a", "d"]);
    expect(hidden).toBe(2);
  });

  it("keeps a busy or open project listed past the limit", () => {
    const { shown, hidden } = sidebarProjects(projects, {}, new Set(["a"]), 2);
    expect(shown.map((p) => p.id)).toEqual(["d", "c", "a"]);
    expect(hidden).toBe(1);
  });
});

describe("later", () => {
  it("picks the later time, or whichever there is", () => {
    expect(later("2026-01-01", "2026-02-01")).toBe("2026-02-01");
    expect(later(undefined, "2026-02-01")).toBe("2026-02-01");
    expect(later(undefined, undefined)).toBeUndefined();
  });

  it("puts pinned projects first, on top of the recent ones", () => {
    const list = ["a", "b", "c", "d"].map(
      (id, i) => ({ id, created_at: `2026-10-0${i + 1}` }) as Project,
    );
    const { shown, hidden } = sidebarProjects(list, {}, new Set(), 2, new Set(["a"]));
    expect(shown.map((p) => p.id)).toEqual(["a", "d", "c"]);
    expect(hidden).toBe(1);
  });
});
