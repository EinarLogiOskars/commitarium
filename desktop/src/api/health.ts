import { request, NetworkError } from "./client";

export interface HealthStatus {
  status: string;
}

/** True when the coordinator answers `GET /health` with `{"status":"ok"}`. */
export async function coordinatorReachable(): Promise<boolean> {
  try {
    const health = await request<HealthStatus>("/health");
    return health.status === "ok";
  } catch (e) {
    if (e instanceof NetworkError) return false;
    // A non-network error (unexpected status/shape) still means "not healthy".
    return false;
  }
}
