import { describe, expect, it } from "vitest";
import { realAppOffer } from "./realApp";
import type { RealApp } from "../ipc";

const here: RealApp = { on_server: false, deployed: false, address: null, last: null };
const server: RealApp = {
  on_server: true,
  deployed: true,
  address: "https://todo.example.com/",
  last: null,
};

describe("the real app's offer", () => {
  it("opens an app on this computer in Commitarium first, until the browser was used last", () => {
    expect(realAppOffer(here, true)).toEqual({ kind: "open", buttons: ["window", "browser"] });
    expect(realAppOffer({ ...here, last: "window" }, true)).toEqual({
      kind: "open",
      buttons: ["window", "browser"],
    });
    expect(realAppOffer({ ...here, last: "browser" }, true)).toEqual({
      kind: "open",
      buttons: ["browser", "window"],
    });
  });

  it("offers the browser on this computer even before the app runs", () => {
    expect(realAppOffer({ ...here, address: null }, true).kind).toBe("open");
  });

  it("says a project not on the Apps page isn't installed yet", () => {
    expect(realAppOffer(here, false)).toEqual({
      kind: "missing",
      text: "Your real app isn't installed yet.",
    });
  });

  it("opens a server's deployed app in the browser at its address", () => {
    expect(realAppOffer(server, true)).toEqual({ kind: "open", buttons: ["browser"] });
    // Deployed apps count, whether or not they are on the Apps page.
    expect(realAppOffer(server, false)).toEqual({ kind: "open", buttons: ["browser"] });
  });

  it("says when a server's app isn't deployed or has no address", () => {
    expect(realAppOffer({ ...server, deployed: false, address: null }, true)).toEqual({
      kind: "missing",
      text: "Your real app isn't deployed yet.",
    });
    expect(realAppOffer({ ...server, address: null }, true)).toEqual({
      kind: "missing",
      text: "Your real app is deployed but has no address yet.",
    });
  });
});
