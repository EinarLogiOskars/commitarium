// The idea a project was created with, kept until the assistant has it, so a
// start that failed can be retried from the Vision page.
const ideaKey = (projectId: string) => `commitarium.vision-idea.${projectId}`;

export function rememberedIdea(projectId: string): string | null {
  try {
    return localStorage.getItem(ideaKey(projectId));
  } catch {
    return null;
  }
}

export function rememberIdea(projectId: string, idea: string | null) {
  try {
    if (idea) localStorage.setItem(ideaKey(projectId), idea);
    else localStorage.removeItem(ideaKey(projectId));
  } catch {
    // The user types it again.
  }
}
