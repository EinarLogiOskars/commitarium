import type { LocalChanges } from "../ipc";

/** The commit, short, in the work order's title: Pull finds it there to know
 * Commitarium has merged the user's work (handoff/project.rs). */
const shortCommit = (commit: string) => commit.slice(0, 12);

export const bringInDocument = (commit: string) => `local-changes-${shortCommit(commit)}.patch`;

export const bringInMessage = (changes: LocalChanges, document: string) =>
  `Bring in the work from my computer: ${changes.commits} local commit${
    changes.commits === 1 ? "" : "s"
  }, as a patch at .commitarium/documents/${document}. Create a work order titled exactly ` +
  `"Bring in local changes ${shortCommit(changes.headCommitId)}" and start it. Its job is to ` +
  `apply the patch onto the current default branch and resolve any conflicts, keeping the ` +
  `intent of both sides. It builds nothing else.`;
