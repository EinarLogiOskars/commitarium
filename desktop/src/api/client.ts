// Coordinator HTTP client. Runs in the frontend and reaches the local
// coordinator through Tauri's HTTP plugin, which is capability-scoped to the
// coordinator's loopback origin (see src-tauri/capabilities/default.json) —
// so this is the only network destination the UI can reach, and CORS does not
// apply.

import { fetch } from "@tauri-apps/plugin-http";
import { connectedServer, TAILSCALE_HINT, throughTailscale } from "../connection";

// The coordinator listens here under Compose. Loopback only, by design. In
// remote mode this is the app's bridge, also on loopback (ADR-040). Both want
// the token below (COORDINATOR_ACCESS_PLAN.md).
export let COORDINATOR_BASE = "http://127.0.0.1:8080";
let coordinatorToken: string | null = null;

/** Points every coordinator call at the bridge to a paired server. */
export function connectBridge(url: string, token: string) {
  COORDINATOR_BASE = url;
  coordinatorToken = token;
}

// On this computer the token is asked for until the stack has made one.
let tokenSource: (() => Promise<string | null>) | null = null;

/** Where this computer's coordinator token comes from. */
export function askCoordinatorToken(source: () => Promise<string | null>) {
  tokenSource = source;
}

/** Added to each coordinator call. */
export async function coordinatorAuth(): Promise<Record<string, string>> {
  if (!coordinatorToken && tokenSource) {
    coordinatorToken = await tokenSource().catch(() => null);
  }
  return coordinatorToken ? { Authorization: `Bearer ${coordinatorToken}` } : {};
}

/** The coordinator's error envelope: {"error":{"code","message"}}. */
export interface ApiErrorBody {
  error: { code: string; message: string };
}

/** A structured coordinator error, carrying its machine-readable code. */
export class ApiError extends Error {
  code: string;
  status: number;
  constructor(code: string, message: string, status: number) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
  }
}

/** Thrown when the coordinator cannot be reached at all (stack down, etc.). */
export class NetworkError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "NetworkError";
  }
}

interface RequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  /** Sent as the Idempotency-Key header when the endpoint requires it. */
  idempotencyKey?: string;
}

export async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = await coordinatorAuth();
  if (options.body !== undefined) headers["Content-Type"] = "application/json";
  if (options.idempotencyKey) headers["Idempotency-Key"] = options.idempotencyKey;

  let response: Response;
  try {
    response = await fetch(`${COORDINATOR_BASE}${path}`, {
      method: options.method ?? "GET",
      headers,
      body: options.body !== undefined ? JSON.stringify(options.body) : undefined,
    });
  } catch (e) {
    throw new NetworkError(`cannot reach coordinator: ${String(e)}`);
  }

  const text = await response.text();
  const parsed = text ? safeJson(text) : undefined;

  if (!response.ok) {
    // A token made since it was read, or none yet: ask again next time.
    if (response.status === 401 && tokenSource) coordinatorToken = null;
    const body = parsed as ApiErrorBody | undefined;
    if (body?.error) throw new ApiError(body.error.code, body.error.message, response.status);
    // A server that does not answer reaches here as an empty 502 from the
    // bridge: said in words.
    if (response.status === 502 && connectedServer()) {
      const hint = throughTailscale() ? ` ${TAILSCALE_HINT}` : "";
      throw new ApiError(
        "unknown_error",
        `Commitarium can't reach the server right now.${hint}`,
        response.status,
      );
    }
    throw new ApiError("unknown_error", `HTTP ${response.status}`, response.status);
  }

  return parsed as T;
}

function safeJson(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}
