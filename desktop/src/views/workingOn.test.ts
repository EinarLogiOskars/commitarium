import { describe, expect, it } from "vitest";
import type { AssistantAction, Conversation } from "../api/assistant";
import { workingOn } from "./workingOn";

const SERVER = new Set(["deploy_app", "add_domain", "set_homelab"]);
const NOW = Date.parse("2026-11-08T12:00:00Z");

function card(over: Partial<AssistantAction>): AssistantAction {
  return {
    id: "a1",
    message_id: "m1",
    type: "deploy_app",
    title: "Add hello.example.com as well",
    status: "proposed",
    asked: true,
    updated_at: "2026-11-08T12:00:00Z",
    ...over,
  } as AssistantAction;
}

function chat(actions: AssistantAction[], status: "idle" | "running" = "idle"): Conversation {
  return {
    status,
    messages: [
      { id: "m0", role: "user", text: "add it", occurred_at: "" },
      { id: "m1", role: "assistant", text: "Adding it.", occurred_at: "" },
    ],
    actions,
  } as unknown as Conversation;
}

describe("the working checklist", () => {
  it("lists what this app applies, and how each went", () => {
    const items = workingOn(
      chat([
        card({}),
        card({ id: "a2", type: "add_domain", title: "Add example.org", status: "failed" }),
        card({ id: "a3", type: "set_homelab", title: "Turn Homelab on" }),
      ]),
      SERVER,
      (id) => id === "a1",
      NOW,
    );
    expect(items).toEqual([
      { id: "a1", title: "Add hello.example.com as well", state: "doing" },
      { id: "a2", title: "Add example.org", state: "failed" },
    ]);
  });

  it("leaves out a card waiting for the user, and one not asked for", () => {
    expect(
      workingOn(
        chat([card({}), card({ id: "a2", asked: false, status: "applied" })]),
        SERVER,
        () => false,
        NOW,
      ),
    ).toEqual([]);
  });

  it("stays while Mayka writes, then gives way", () => {
    const done = [card({ status: "applied" })];
    expect(workingOn(chat(done, "running"), SERVER, () => false, NOW + 600_000)).toHaveLength(1);
    expect(workingOn(chat(done), SERVER, () => false, NOW + 10_000)).toHaveLength(1);
    expect(workingOn(chat(done), SERVER, () => false, NOW + 600_000)).toEqual([]);
  });

  it("belongs to Mayka's latest reply", () => {
    const later = chat([card({ status: "applied" })], "running");
    later.messages.push({ id: "m2", role: "assistant", text: "Done.", occurred_at: "" });
    expect(workingOn(later, SERVER, () => false, NOW)).toEqual([]);
  });
});
