import { request } from "./client";

// The files attached to a project (planning mode): specs, API docs, sample
// exports. The assistant, its helpers, and the agents read them in their
// checkouts; they are never committed.

export interface ProjectDocument {
  name: string;
  size: number;
  added_at: string;
}

/** The largest document the coordinator accepts. */
export const MAX_DOCUMENT_BYTES = 5 * 1024 * 1024;

const documentsPath = (id: string) => `/api/v1/projects/${encodeURIComponent(id)}/documents`;

export const listDocuments = (id: string): Promise<{ documents: ProjectDocument[] }> =>
  request(documentsPath(id));

/** Attach a file, replacing one of the same name. */
export const addDocument = async (id: string, file: File): Promise<ProjectDocument> => {
  const bytes = new Uint8Array(await file.arrayBuffer());
  let binary = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return request(documentsPath(id), {
    method: "POST",
    body: { name: file.name, content_base64: btoa(binary) },
  });
};

/** The start of a text document; preview is empty for any other file. */
export const previewDocument = (
  id: string,
  name: string,
): Promise<{ name: string; preview: string; truncated: boolean }> =>
  request(`${documentsPath(id)}/${encodeURIComponent(name)}`);

export const removeDocument = (id: string, name: string): Promise<void> =>
  request(`${documentsPath(id)}/${encodeURIComponent(name)}`, { method: "DELETE" });
