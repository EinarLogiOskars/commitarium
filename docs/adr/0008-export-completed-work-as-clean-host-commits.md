# ADR-008: Export completed work as clean host commits

- Status: Accepted
- Date: 2026-09-09

## Context

Forgejo is Commitarium's protected engineering environment. Its feature branch
and pull request intentionally retain agent-authored commits, planning decisions,
review findings, corrections, validation evidence, and approval. That history is
valuable inside Commitarium, but many users will not want those internal agent
identities, intermediate commits, or Forgejo-specific details copied into their
normal Git repository.

Git pull-request comments and Forgejo database records are not part of a Git
commit and would not be transferred by an ordinary push. Commit author and
committer identities, commit messages, signatures, parent relationships, and
merge commits are Git data, however. Directly pushing the internal Forgejo merge
would therefore expose at least some of the internal workflow history.

Users also need control over two separate moments: when accepted work becomes
available in their local repository, and when their local repository sends it to
GitHub, GitLab, or another upstream. Most users may choose to perform both
immediately, but local synchronization must not imply an external push.

## Decision

External handoff is a trusted-host capability with two independently requestable
operations:

1. **Synchronize locally** creates or verifies one clean export commit in the
   user's selected local repository from the exact approved feature change.
2. **Push upstream** sends only that previously verified local export commit to
   the user's configured Git remote.

A user preference may chain these operations into one action. The second
operation begins only after the first has durably identified its exact result.
Retrying either operation must be idempotent.

The default export is one commit per completed feature. It is created from the
net change between the feature's recorded internal base and its exact approved
head, not by copying the internal commit objects. The export uses a Git author
and committer identity explicitly selected from the user's trusted-host
configuration. If that identity is missing, synchronization stops for user
input. Agent identities are not silently rewritten one commit at a time.

The clean commit contains the resulting source changes and a user-facing commit
message. It does not contain Forgejo comments, review records, internal workflow
identifiers, agent attribution, internal merge messages, or links to the private
Forgejo audit trail unless the user explicitly opts into such metadata later.
Commitarium keeps the audit trail in Forgejo and may retain the handoff mapping
in trusted local application state.

Because changing authorship, message, or parents changes a Git commit hash, the
internal Forgejo revision and clean local commit are expected to have different
IDs. The handoff record must bind at least:

- project and feature identity;
- exact approved internal base and head revisions;
- exact confirmed Forgejo merge revision;
- destination local repository and target branch;
- generated clean local commit ID; and
- upstream remote and pushed commit ID when a push occurs.

The first MVP implementation should require an unambiguous base relationship.
It prepares the clean commit in a temporary host worktree so the user's active
working tree is not used as scratch space. It may update the selected local
branch only when the repository, expected base, checked-out worktree state, and
result are all safe and consistent. Dirty worktrees, diverged branches, missing
commits, ambiguous prior synchronization, patch conflicts, or changed reviewed
content stop for user review. It never resets, cleans, force-pushes, or silently
resolves a conflict.

Before pushing, the trusted host verifies that the selected local commit is the
recorded synchronization result and that the upstream branch has not advanced
unexpectedly. Authentication comes from the user's host Git credential helper
or an explicitly configured host integration. No upstream URL containing
credentials and no GitHub, GitLab, or other external credential enters the
coordinator or agent containers.

Preserving the internal agent commit sequence may be offered later as an
advanced export mode, but it is not the default and is outside the initial
handoff implementation.

Projects originally imported from a plain folder use the same trusted-host
boundary but do not have a local Git branch on which to create the clean commit.
For those projects, local synchronization temporarily indexes the original
folder without creating `.git`. Its non-ignored content must exactly match the
completed feature's internal base tree before any write. Commitarium verifies
and dry-runs the exact base-to-approved patch, records a durable prepared
receipt, applies it to the folder, and verifies the complete approved tree.
Ignored dependencies and build outputs remain outside the handoff and are not
removed.

The prepared receipt distinguishes a safe unchanged retry from a result that
was fully applied before the completion record was saved. If the current folder
matches neither the base nor approved tree, synchronization stops for user
inspection rather than overwriting or attempting to infer ownership of the
changes. A plain-folder handoff produces no local commit and cannot use the
separate upstream-push operation unless the user later makes that folder a Git
repository through an explicit workflow.

## Consequences

### Positive

- The user keeps a conventional repository history with one intentional commit
  per accepted feature.
- Forgejo preserves the detailed agent collaboration and review audit without
  publishing it to the user's upstream.
- Local inspection and external publication remain separate choices.
- The same provider-neutral Git flow works with GitHub, GitLab, self-hosted Git,
  or another standard remote.
- External credentials remain outside the container trust boundary.

### Negative

- Internal and external commit IDs differ, so Commitarium must retain and verify
  an explicit handoff mapping.
- A later feature cannot assume that the Forgejo base commit ID is also present
  on the user's clean branch; synchronization must compare the recorded mapping
  and content relationship.
- Applying an approved change over a destination branch that advanced in the
  meantime may require renewed validation or user conflict resolution.
- Reproducing signed commits requires a separate trusted-host signing design.

## Alternatives considered

### Push the Forgejo merge commit directly

This preserves identical commit IDs and is mechanically simple, but it also
preserves internal author/committer identities, messages, parent history, and
possibly a Forgejo-generated merge commit in the user's repository.

### Copy files into the user's working tree

This makes the result visible but can overwrite uncommitted work, loses a safe
Git boundary, and is difficult to recover idempotently after interruption.

### Synchronize locally and always push immediately

This removes the user's chance to inspect or amend the local result before an
external side effect. Chaining remains available as an explicit preference.

### Give agents external-repository credentials

This would let an agent push directly, but it breaks the isolation boundary and
allows mistakes or compromised project tooling to affect the user's upstream.

## Reconsideration criteria

Reconsider the clean-export default if users consistently need the internal
commit sequence upstream, if a supported forge can create the exact desired
user-authored clean commit without weakening the trust boundary, or if reliable
content/base mapping proves too confusing. Any replacement must preserve
separate local-sync and external-push consent and must not expose upstream
credentials to containers.
