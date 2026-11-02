import { describe, expect, it } from "vitest";
import { slug } from "./DeployCard";

describe("slug", () => {
  it("makes an address name from a project's name", () => {
    expect(slug("Simple To-Do")).toBe("simple-to-do");
    expect(slug("  Mañana en Islandia! ")).toBe("manana-en-islandia");
    expect(slug("¡Vamos, Vikingo!")).toBe("vamos-vikingo");
  });
});
