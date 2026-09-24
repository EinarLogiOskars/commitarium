import { request } from "./client";
import type { AttentionResponse } from "./types";

/** One cross-project snapshot for the review inbox and notification poller. */
export const listAttention = (): Promise<AttentionResponse> => request("/api/v1/attention");
