import { invoke } from "@tauri-apps/api/core";
import { fetch } from "@tauri-apps/plugin-http";
import { COORDINATOR_BASE, NetworkError, coordinatorAuth, request } from "./client";

// What Mayka's helpers make (HELPERS_PLAN.md): reports, decks, drafts. They
// are kept per project, or for the chats outside any project
// ("commitarium"), and shown in the chat and the Outputs card.

export interface OutputFile {
  name: string;
  size: number;
  /** When its latest version was made. */
  made_at: string;
  /** The helper that made it, and its task. */
  helper?: string;
  title?: string;
  /** The helper's own name for it, when another file had its name first. */
  source?: string;
  /** Its latest version's number, from 1 (H8). */
  version?: number;
  /** Its helper is at work on it now; kept: the helper still keeps its
   * session and directory, until the user marks the file Done. */
  working?: boolean;
  kept?: boolean;
}

/** One version of an output. */
export interface OutputVersion {
  number: number;
  size: number;
  made_at: string;
}

/** A file in a helper's outputs/ folder as it is now, while it builds it. */
export interface HelperFile {
  name: string;
  size: number;
  made_at: string;
}

/** Fired when a scope's outputs may have changed, so the card reads again. */
export const OUTPUTS_CHANGED = "commitarium:outputs-changed";

const outputsPath = (owner: string) => `/api/v1/outputs/${encodeURIComponent(owner)}`;

export const listOutputs = (owner: string): Promise<{ outputs: OutputFile[] }> =>
  request(outputsPath(owner));

export const deleteOutput = (owner: string, name: string): Promise<void> =>
  request(`${outputsPath(owner)}/${encodeURIComponent(name)}`, { method: "DELETE" });

const outputPath = (owner: string, name: string) =>
  `${outputsPath(owner)}/${encodeURIComponent(name)}`;

/** An output's versions, newest first. */
export const listOutputVersions = (
  owner: string,
  name: string,
): Promise<{ versions: OutputVersion[] }> => request(`${outputPath(owner, name)}/versions`);

/** Makes an older version the latest, as a new version. */
export const restoreOutputVersion = (
  owner: string,
  name: string,
  version: number,
): Promise<OutputFile> =>
  request(`${outputPath(owner, name)}/versions/${version}/restore`, { method: "POST" });

/** Marks an output Done: its helper lets go of its session and directory. */
export const markOutputDone = (owner: string, name: string): Promise<void> =>
  request(`${outputPath(owner, name)}/done`, { method: "POST" });

const helperFilesPath = (owner: string, helper: string) =>
  `/api/v1/helper-files/${encodeURIComponent(owner)}/${encodeURIComponent(helper)}`;

/** What a helper has in its outputs/ folder now. */
export const listHelperFiles = (owner: string, helper: string): Promise<{ files: HelperFile[] }> =>
  request(helperFilesPath(owner, helper));

async function readBlob(path: string): Promise<Blob> {
  let response: Response;
  try {
    response = await fetch(`${COORDINATOR_BASE}${path}`, { headers: coordinatorAuth() });
  } catch (e) {
    throw new NetworkError(`cannot reach coordinator: ${String(e)}`);
  }
  if (!response.ok) throw new Error("The file is not there any more.");
  return response.blob();
}

/** An output's contents: its latest version, or the one numbered. */
export const readOutput = (owner: string, name: string, version?: number): Promise<Blob> =>
  readBlob(version ? `${outputPath(owner, name)}/versions/${version}` : outputPath(owner, name));

/** A file of a helper's outputs/ folder as it is now. */
export const readHelperFile = (owner: string, helper: string, name: string): Promise<Blob> =>
  readBlob(`${helperFilesPath(owner, helper)}/${encodeURIComponent(name)}`);

/** Asks the chat beside it to open an output in its panel; false when no
 * chat of that owner is showing, so the caller opens it itself. */
export const OPEN_OUTPUT = "commitarium:open-output";

export interface OpenOutputDetail {
  owner: string;
  /** The output, or else images to step through. */
  name?: string;
  images?: string[];
}

export function openOutputBeside(owner: string, name?: string, images?: string[]): boolean {
  const event = new CustomEvent<OpenOutputDetail>(OPEN_OUTPUT, {
    detail: { owner, name, images },
    cancelable: true,
  });
  window.dispatchEvent(event);
  return event.defaultPrevented;
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
