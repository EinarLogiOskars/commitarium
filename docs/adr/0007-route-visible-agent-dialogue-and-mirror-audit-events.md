# ADR-007: Route visible agent dialogue and mirror agreed engineering records

- Status: Accepted
- Date: 2026-09-09

## Context

Commitarium's primary collaboration is not a sequence of unrelated agent jobs.
One lead agent helps the user define a goal, discusses a plan with a second
agent, implements the accepted plan, and answers that agent's review findings.
The second agent acts first as a planning consultant and later as the
independent reviewer.

The user must be able to watch this agent-to-agent discussion as it happens and
join when needed. Forgejo must separately retain a concise, concrete record of
the accepted goal, planning decisions, implementation, review findings,
responses, validation, and approval. Using only Forgejo comments as the live
transport would make the workflow slow and awkward. Storing only coordinator
summaries would prevent the user from seeing what the agents actually said to
each other.

Provider-native transcripts may also contain private implementation details,
provider protocol traffic, or reasoning that is neither intended nor safe to
publish. The visible conversation therefore consists of explicit messages the
agents author for each other and the user, not raw provider-native transcripts
or hidden chain-of-thought.

## Decision

Each feature workflow has two durable logical agent sessions:

- The **lead session** remains continuous across goal drafting, collaborative
  planning, implementation, and responses to review findings.
- The **reviewer session** remains continuous across planning consultation,
  implementation review, and verification of the lead's responses.

The lead and reviewer roles are independent of provider. A user may assign
Codex or Claude Code to either role, including using the same provider for both
roles. They remain distinct sessions and Forgejo identities even when they use
the same provider.

The coordinator is a transparent message router and workflow controller. It:

1. persists each explicit user-to-agent and agent-to-agent message in one
   ordered feature conversation;
2. records sender, recipient, workflow phase, delivery state, and timestamp;
3. delivers the message text to the recipient's resumable provider session
   without replacing it with a coordinator-generated summary;
4. streams the same conversation to the user interface as it happens;
5. lets the user address either agent, join a discussion, pause it, or resolve
   a disagreement; and
6. bounds autonomous planning and review exchanges so an unresolved discussion
   eventually waits for the user.

The coordinator may add a clearly identified recovery or workflow briefing when
resuming a provider session, but it must preserve the other participant's
authored message. Delivery is turn-based; providers do not receive network
access to each other or invoke each other directly.

After the user accepts the goal, the coordinator creates the internal feature
branch and draft Forgejo pull request before collaborative planning begins. The
live conversation and Forgejo audit trail have different purposes:

- The coordinator conversation is the complete user-visible exchange needed
  to follow and control the active workflow.
- Forgejo is the durable, human-readable engineering record. Intermediate
  planning proposals and objections stay in the Commitarium conversation. Once
  the agents agree, Forgejo receives the final plan and its important rationale.
  During formal review it also receives findings, implementer responses,
  disagreements and decisions, test results, approval, and relevant commit
  references under the correct identity.

Routine progress chatter and low-level tool activity need not become pull
request comments. Forgejo audit entries link back to stable conversation or
workflow-event identifiers where practical, so a user can correlate the concise
record with the fuller conversation.

Feature work occurs on Forgejo-backed workspaces inside container-managed
storage. Agent containers receive only the provider state, project workspace,
and scoped Forgejo identity needed for their role. They do not receive GitHub
credentials. Any later synchronization from Forgejo to GitHub is an explicit
user-controlled operation at the trusted host boundary.

## Consequences

### Positive

- The user can observe genuine agent collaboration instead of reconstructed
  summaries.
- Both agents retain context across the complete feature lifecycle.
- Provider choice does not alter orchestration behavior.
- Forgejo remains useful as an audit trail without being overloaded with every
  transient progress event.
- The same ordered conversation can drive a conventional chat interface now
  and richer agent visualizations later.
- Container and credential boundaries keep iterative work away from the user's
  host checkout and GitHub repository.

### Negative

- The coordinator needs a durable run-level conversation model in addition to
  per-session activity.
- Message delivery must be recoverable without accidentally delivering the same
  agent turn twice.
- Mirrored Forgejo entries require stable identity and correlation metadata.
- Two persistent provider sessions may retain more context and consume more
  provider storage than short phase-specific sessions.

## Alternatives considered

### Use Forgejo comments as the only agent communication channel

This produces an audit trail automatically, but it makes live collaboration
dependent on polling and turns routine conversation into permanent pull-request
noise.

### Relay only coordinator-generated summaries

This reduces stored text but hides the actual discussion, can change meaning,
and gives the user too little evidence to understand agent decisions.

### Start a new provider session for every workflow phase

This simplifies individual invocations but loses the continuity the lead and
reviewer need to remember planning tradeoffs, implementation decisions, and
prior review responses.

### Publish complete provider-native transcripts

Native transcripts are provider-specific and can include protocol details,
private reasoning, secrets, and unrelated tool output. They are not the same as
the explicit messages intentionally exchanged by participants.

## Reconsideration criteria

Reconsider the two-session model if a supported provider cannot safely resume a
conversation across workflow phases, or if measured context growth makes a
bounded handoff to a fresh session materially safer or more reliable. Any such
handoff must remain explicit and preserve the visible conversation and Forgejo
audit record.
