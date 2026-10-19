// Developer mode shows the projects, the audit viewer, and the system's
// status (App.tsx). The assistant hears whether it is on with each message,
// so a new app stays self-contained without it (ADR-035).
export const DEV_MODE_KEY = "commitarium.devMode";

export function developerModeOn(): boolean {
  try {
    return localStorage.getItem(DEV_MODE_KEY) === "1";
  } catch {
    return false;
  }
}
