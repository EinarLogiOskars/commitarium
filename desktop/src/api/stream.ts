// Server-Sent Events client for the coordinator's streaming endpoints
// (…/events/stream, …/messages/stream, …/sessions/{id}/events/stream).
//
// The browser-native EventSource is unusable here: it opens its own connection,
// which is cross-origin to the coordinator and blocked (the coordinator sends no
// CORS headers — the whole reason the app talks through Tauri's HTTP plugin). So
// we stream through the plugin's fetch instead, whose Response.body is a real
// ReadableStream, and parse the SSE wire format ourselves. Reconnects with
// backoff and resumes from Last-Event-ID so no events are missed across drops.

import { fetch } from "@tauri-apps/plugin-http";
import { COORDINATOR_BASE } from "./client";

export interface StreamEvent {
  id?: string;
  event?: string;
  data: string;
}

export interface StreamHandle {
  close: () => void;
}

export interface StreamOptions {
  onEvent: (event: StreamEvent) => void;
  onError?: (error: unknown) => void;
  /** Resume point; updated automatically from each event's id thereafter. */
  lastEventId?: string;
}

/** Subscribe to a coordinator SSE path. Returns a handle whose close() ends the
 * subscription and aborts the in-flight request. */
export function streamEvents(path: string, opts: StreamOptions): StreamHandle {
  let closed = false;
  let controller: AbortController | null = null;
  let lastId = opts.lastEventId;
  let backoffMs = 1000;

  const run = async () => {
    while (!closed) {
      controller = new AbortController();
      try {
        const headers: Record<string, string> = { Accept: "text/event-stream" };
        if (lastId) headers["Last-Event-ID"] = lastId;
        const response = await fetch(`${COORDINATOR_BASE}${path}`, {
          headers,
          signal: controller.signal,
        });
        if (!response.ok || !response.body) {
          throw new Error(`stream failed: HTTP ${response.status}`);
        }
        backoffMs = 1000; // healthy connection — reset backoff
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          // Frames are separated by a blank line; tolerate CRLF.
          buffer = buffer.replace(/\r\n/g, "\n");
          let sep: number;
          while ((sep = buffer.indexOf("\n\n")) !== -1) {
            const frame = buffer.slice(0, sep);
            buffer = buffer.slice(sep + 2);
            const parsed = parseFrame(frame);
            if (parsed?.retry != null) backoffMs = parsed.retry;
            if (!parsed || parsed.data == null) continue;
            if (parsed.id) lastId = parsed.id;
            opts.onEvent({ id: parsed.id, event: parsed.event, data: parsed.data });
          }
        }
      } catch (error) {
        if (closed) return;
        opts.onError?.(error);
      }
      if (closed) return;
      await sleep(backoffMs);
      backoffMs = Math.min(backoffMs * 2, 15000);
    }
  };

  void run();
  return {
    close: () => {
      closed = true;
      controller?.abort();
    },
  };
}

interface ParsedFrame {
  id?: string;
  event?: string;
  data?: string;
  retry?: number;
}

// Parse one SSE frame per the wire format: `field: value` lines, `:` comments,
// multiple `data:` lines joined by "\n". Returns undefined for comment-only or
// empty frames (e.g. keep-alives).
function parseFrame(frame: string): ParsedFrame | undefined {
  const out: ParsedFrame = {};
  const dataLines: string[] = [];
  let sawField = false;
  for (const line of frame.split("\n")) {
    if (line === "" || line.startsWith(":")) continue; // comment / keep-alive
    const colon = line.indexOf(":");
    const field = colon === -1 ? line : line.slice(0, colon);
    // A leading space after the colon is stripped, per spec.
    let value = colon === -1 ? "" : line.slice(colon + 1);
    if (value.startsWith(" ")) value = value.slice(1);
    switch (field) {
      case "id":
        out.id = value;
        sawField = true;
        break;
      case "event":
        out.event = value;
        sawField = true;
        break;
      case "data":
        dataLines.push(value);
        sawField = true;
        break;
      case "retry": {
        const n = Number(value);
        if (Number.isFinite(n)) out.retry = n;
        sawField = true;
        break;
      }
      default:
        break; // ignore unknown fields
    }
  }
  if (!sawField) return undefined;
  if (dataLines.length > 0) out.data = dataLines.join("\n");
  return out;
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
