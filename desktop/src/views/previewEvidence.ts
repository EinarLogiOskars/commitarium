import type { ServiceEvidence } from "../api/types";

// What each container of a preview did (ADR-028): from a preview that failed
// after its containers started, or from Commitarium's preview check.

/** Whether the service is what failed: it stopped with an error, keeps
 * restarting, or reports itself unhealthy. */
export const isFailing = (s: ServiceEvidence): boolean =>
  s.exit_code !== 0 || s.state === "restarting" || s.state === "dead" || s.health === "unhealthy";

/** The services, failing ones first. */
export const failingFirst = (services: ServiceEvidence[]): ServiceEvidence[] => [
  ...services.filter(isFailing),
  ...services.filter((s) => !isFailing(s)),
];

/** A service's state in a few words. */
export function serviceState(s: ServiceEvidence): string {
  if (s.state === "restarting") return `keeps restarting (exit code ${s.exit_code})`;
  if (s.exit_code !== 0) return `stopped with exit code ${s.exit_code}`;
  if (s.health === "unhealthy") return "running, but unhealthy";
  if (s.state === "exited") return "finished";
  if (s.health === "starting") return "starting";
  return s.state || "unknown";
}

/** What fails, in plain words for someone who does not write code; null when
 * no container failed. Matches what the coordinator says when it blocks. */
export function plainFailure(services: ServiceEvidence[] | undefined): string | null {
  const failing = (services ?? []).find(isFailing);
  if (!failing) return null;
  const part = `The “${failing.service}” part of the app`;
  if (failing.health === "unhealthy") return `${part} starts but reports that it is not working.`;
  if (failing.state === "restarting") return `${part} keeps crashing and restarting.`;
  return `${part} stops right after it starts (exit code ${failing.exit_code}).`;
}
