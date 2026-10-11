// The vision conversation in progress for a project, so it reopens where it
// was: after a restart, and right after a new project is created.
const sessionKey = (projectId: string) => `commitarium.vision-session.${projectId}`;

export function rememberedSession(projectId: string): string | null {
  try {
    return localStorage.getItem(sessionKey(projectId));
  } catch {
    return null;
  }
}

export function rememberSession(projectId: string, sessionId: string | null) {
  try {
    if (sessionId) localStorage.setItem(sessionKey(projectId), sessionId);
    else localStorage.removeItem(sessionKey(projectId));
  } catch {
    // The conversation is only reopened less conveniently.
  }
}
