# ADR-009: Let agents own internal forge actions

- Status: Accepted
- Date: 2026-09-10

## Context

Commitarium gives agents an isolated development environment so they can use
normal software-development tools without receiving access to the user's host
repository or external Git provider. The initial implementation instead made
the coordinator create and push the implementation commit after a separate
user approval. That removed the agent's Forgejo identity from repository
history, added an unnecessary user interruption before review, and made the
coordinator responsible for developer work.

The user should see the lead and reviewer discussion live in Commitarium, while
Forgejo should contain a concise engineering record written under the identity
of the agent responsible for each action. Copying the conversation into the
pull request would duplicate information and create a noisy audit trail.

## Decision

Each agent profile receives a separate, narrowly scoped Forgejo identity and
credential inside its worker environment. The credential is private to that
profile and is not placed in the coordinator database, provider transcript,
repository configuration, or user-facing activity.

Within the assigned managed feature workspace:

- the lead may edit, test, commit, and push the feature branch;
- the lead writes the agreed plan, implementation summary, validation evidence,
  and responses to findings to the pull request;
- the reviewer inspects the exact pushed revision, posts structured findings or
  approval, and verifies later resolutions; and
- both agents use their normal Git and Forgejo tools under their own identities.

The agents communicate directly through the coordinator's visible turn-based
conversation. They do not copy that conversation into Forgejo. Instead, their
instructions require them to turn relevant conclusions into normal engineering
artifacts such as a final plan, review finding, response, test result, or
approval.

The coordinator continues to own workflow policy, session routing, recovery,
and lifecycle transitions. It may bootstrap the managed branch, checkout, and
draft pull request, and it may perform the final protected-branch merge under
the configured merge policy. It does not create implementation commits, push
agent changes, or post ongoing review content on an agent's behalf.

An agent reports structured completion information through its worker boundary,
including the commit and pull-request revision it believes it produced. Before
starting review, accepting approval, or merging, the coordinator independently
checks the durable Git and Forgejo state. Agent reports are claims to verify,
not workflow authority.

If a worker stops while a Git or Forgejo action may have happened, recovery
inspects the feature branch and pull request before asking the original provider
session to continue. It never starts a replacement agent or repeats an uncertain
side effect merely because the coordinator did not receive a response.

The internal default branch remains protected. Agent credentials are scoped to
the internal Forgejo service and never provide GitHub or other external-repository
access. Exporting completed work to the user's repository remains a separate
trusted-host operation under ADR-008.

## Consequences

### Positive

- Forgejo history shows which agent authored a commit, finding, response, or
  approval.
- Implementation and review can continue without an unnecessary user approval
  between every agent turn.
- Agents retain familiar developer workflows inside the intended isolation
  boundary.
- The coordinator stays focused on orchestration and verification rather than
  acting as a Git or pull-request proxy.
- The live conversation and the concise pull-request audit serve different,
  clear purposes.

### Negative

- A malfunctioning agent can damage its feature branch or pull-request content.
  The limited credential, protected default branch, and disposable workspace
  reduce but do not remove this risk.
- Worker recovery must reconcile external Git and Forgejo state because those
  actions do not occur in the same transaction as coordinator state.
- Separate lead and reviewer identities require additional Forgejo account and
  credential provisioning.

## Alternatives considered

### Let the coordinator commit and push after user approval

This was implemented temporarily and then removed. It hides agent authorship,
interrupts the autonomous implementation-review loop, and puts developer
responsibilities in the coordinator.

### Proxy every Forgejo action through coordinator endpoints

This could centralize credentials and validation, but it would recreate much of
Forgejo's API, add latency, and unnecessarily restrict capable agents. The
coordinator still verifies important results before changing workflow state.

### Copy the agent conversation into the pull request

This would produce a complete record in one place, but it duplicates the live
conversation and obscures the structured plan and review history with transient
discussion. Only the resulting engineering records belong in Forgejo.

## Reconsideration criteria

Reconsider direct agent Forgejo access if scoped credentials cannot protect the
default branch and unrelated repositories, or if provider behavior shows that
independent verification cannot make external actions sufficiently recoverable.
