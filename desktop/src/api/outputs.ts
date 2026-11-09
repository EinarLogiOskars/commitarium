import { invoke } from "@tauri-apps/api/core";
import { fetch } from "@tauri-apps/plugin-http";
import { COORDINATOR_BASE, NetworkError, coordinatorAuth, request } from "./client";

// What Mayka's helpers make (HELPERS_PLAN.md): reports, decks, drafts. They
// are kept per project, or for the chats outside any project
// ("commitarium"), and shown in the chat and the Outputs card.

export interface OutputFile {
  name: string;
  size: number;
  made_at: string;
  /** The helper that made it, and its task. */
  helper?: string;
  title?: string;
}

/** Fired when a scope's outputs may have changed, so the card reads again. */
export const OUTPUTS_CHANGED = "commitarium:outputs-changed";

const outputsPath = (owner: string) => `/api/v1/outputs/${encodeURIComponent(owner)}`;

export const listOutputs = (owner: string): Promise<{ outputs: OutputFile[] }> =>
  request(outputsPath(owner));

export const deleteOutput = (owner: string, name: string): Promise<void> =>
  request(`${outputsPath(owner)}/${encodeURIComponent(name)}`, { method: "DELETE" });

/** An output's contents. */
export async function readOutput(owner: string, name: string): Promise<Blob> {
  let response: Response;
  try {
    response = await fetch(`${COORDINATOR_BASE}${outputsPath(owner)}/${encodeURIComponent(name)}`, {
      headers: coordinatorAuth(),
    });
  } catch (e) {
    throw new NetworkError(`cannot reach coordinator: ${String(e)}`);
  }
  if (!response.ok) throw new Error("The file is not there any more.");
  return response.blob();
}

/** Saves a file where the user chooses; false when they cancelled. */
export const saveOutput = async (name: string, contents: Blob): Promise<boolean> =>
  invoke("save_output", new Uint8Array(await contents.arrayBuffer()), {
    headers: { "x-name": name },
  });

/** Opens the system print dialog on a page of its own, where the user saves
 * it as a PDF. */
export const printOutput = (title: string, html: string): Promise<void> =>
  invoke("print_output", { title, html });

/** Opens a file the app cannot show in the user's default app for it. */
export const openOutput = async (name: string, contents: Blob): Promise<void> =>
  invoke("open_output", new Uint8Array(await contents.arrayBuffer()), {
    headers: { "x-name": name },
  });
