# ADR-006: Separate native transcripts from redacted activity

- Status: Accepted
- Date: 08-09-2026

## Context

Real Codex and Claude Code sessions produce several kinds of conversational and
operational data. Provider-native transcripts preserve the exact state required
to resume a provider conversation. The Commitarium UI needs useful live
activity and history. Forgejo needs a durable human-readable record of plans,
reviews, decisions, and repository changes.

Those records have different purposes and risk profiles. Copying an entire
provider transcript into SQLite and Forgejo would duplicate source code, tool
output, and potentially sensitive values. Disabling provider transcript writes
would make restart-safe provider resume unavailable.

Codex stores local session transcripts beneath `CODEX_HOME` and supports bounded
history persistence. Claude Code stores plaintext JSONL containing messages,
tool uses, and tool results beneath `CLAUDE_CONFIG_DIR`; local persistence is
what enables resume and is cleaned after 30 days by default. Both providers warn
that local logs or transcripts can contain sensitive information.

Redaction is not context summarization. Summarization reduces conversation size
or creates a recovery briefing. Redaction removes or suppresses sensitive data
before it crosses a persistence or display boundary.

## Decision

Commitarium will maintain three deliberately separate records:

1. **Provider-native transcript:** private provider state used only for native
   session resume, stored in the agent profile's provider-state volume.
2. **Coordinator session activity:** normalized, bounded, redacted events used
   by the public API, live UI, recovery logic, and operational history.
3. **Forgejo audit trail:** concise plans, review findings, decisions, rationale,
   commits, CI evidence, and merge history associated with the internal pull
   request.

No layer is treated as a complete copy of another. Durable repository and
Forgejo state remain authoritative when they disagree with conversational
memory.

### Provider-native transcripts

Provider-native transcript and session files remain enabled because real
provider resume depends on them. They stay inside the private provider-state
volume selected in ADR-005 and are never served by the coordinator API, copied
into coordinator SQLite, attached to Forgejo, or included in routine support
bundles.

These files may contain unredacted prompts, source fragments, command output,
tool results, and values that a provider process observed. Commitarium will not
rewrite native transcript files in place: doing so could corrupt provider state
or change what a resumed agent remembers. Their protection comes from volume
isolation, least-privilege inputs, retention limits, and explicit deletion.

Native transcript retention defaults to:

- No cleanup while a workflow is active, paused, waiting for the user, or
  otherwise eligible for recovery.
- A 30-day recovery grace period after a workflow becomes terminal.
- Immediate removal when the user explicitly purges the session or deletes its
  agent profile, after warning that native resume will no longer be possible.

Provider cleanup settings must be configured so they cannot delete a session
still referenced by a recoverable workflow. If a provider cannot guarantee
that behavior, its adapter cannot be enabled until Commitarium owns a tested
retention mechanism around the native state. Provider-wide history files are
size-bounded where supported.

Removing native session data does not remove Forgejo history or the redacted
coordinator audit record. Ordinary backups and exports continue to exclude
provider-state volumes by default.

### Normalization and redaction boundary

Raw provider output is accepted only inside the provider worker. The data path
is:

```text
provider CLI output
    -> worker parses and classifies the provider event
    -> worker removes known secrets and disallowed fields
    -> redacted worker event spool
    -> authenticated internal transport
    -> coordinator validates and filters again
    -> SQLite
    -> SSE and public API
```

The worker performs the first pass because it has access to the materialized
secret values that must never reach the coordinator. The coordinator performs a
second independent pass for structural rules and recognized credential patterns
before persistence or publication. Application logs use the same already
redacted representation rather than logging raw requests or provider frames.

The normalized activity model permits:

- User-visible assistant messages and provider-approved reasoning summaries.
- High-level activity such as reading, editing, testing, waiting, or reviewing.
- Repository-relative paths, command names, working-directory identity, exit
  status, duration, and bounded redacted output.
- Input requests, pause/continue acknowledgements, terminal summaries, and
  recovery assessments.

It excludes:

- Private chain-of-thought, hidden reasoning, and raw reasoning deltas.
- Provider protocol frames, credential objects, request headers, cookies, and
  authentication diagnostics containing values.
- Full environment dumps and secret-source files.
- Unbounded file contents, diffs, command output, or binary data already
  represented by the worktree, Forgejo, or a dedicated artifact.

Large permitted values are truncated before persistence with their original
byte count and a visible truncation marker. Truncation is an output-volume rule,
not redaction, and the UI must distinguish the two.

### Redaction rules

Redaction proceeds from strongest knowledge to weakest heuristic:

1. Remove structured fields that are not explicitly allowed for that event
   type.
2. Replace exact materialized secret values known to the worker with the fixed
   marker `[REDACTED]`.
3. Remove values from authentication headers, credential URLs, cookies, and
   provider credential structures.
4. Apply maintained pattern detectors for recognized token, private-key, and
   connection-string forms.
5. Normalize container-internal absolute workspace paths to project-relative
   paths where possible.

Redaction markers never contain the secret's name, prefix, original length, or a
hash derived from its value. A redaction result may record only a count and
broad non-secret category for diagnostics.

Pattern matching is defense in depth, not a guarantee that arbitrary unknown
secrets will be recognized. The primary protection remains not giving a process
credentials or files it does not need. Users are directed to the environment
manager rather than pasting credentials into session chat.

If an event cannot be parsed, classified, bounded, or filtered safely, the
worker does not spool or forward its raw content. It emits a synthetic
redaction-failure event containing no rejected payload and pauses the session
for user review. The same fail-closed behavior applies if coordinator validation
fails. Raw content is never persisted as a diagnostic fallback.

### Coordinator retention

Redacted coordinator session activity is the operational history presented to
the user and used during recovery. It is retained with the project until the
user explicitly deletes or purges the project. This initial default favors a
complete local audit and can become a configurable retention policy later.

The worker's redacted event spool is delivery state, not a fourth transcript.
Events remain until the coordinator durably acknowledges their sequence. After
acknowledgement they may be compacted, while terminal results and the minimal
attempt journal remain through the native recovery grace period.

User messages and commands pass through coordinator filtering before they are
stored or delivered. Obvious credential material is rejected with guidance to
use the environment manager. Because heuristic detection cannot identify every
secret, the UI also warns that chat is not a credential-entry surface.

### Forgejo audit trail

Forgejo receives only the information needed to understand and review the
feature:

- Accepted plan revisions and material deviations.
- Review findings, responses, disagreement, and decisions.
- Recovery assessments and user approvals.
- Commits, diffs, CI evidence, review approval, and merge result.

Routine worker chatter, full prompts, full command output, and provider-native
transcripts are not posted. Forgejo-bound content passes through the same
coordinator redaction boundary before publication.

### Diagnostics and export

Worker and coordinator logs contain identifiers, state transitions, durations,
counts, and redacted error summaries by default. Plaintext provider debug logs,
prompt logging, and verbose protocol-frame logging remain disabled.

Temporary diagnostic logging requires an explicit user action, a visible expiry,
and a dedicated sensitive-data warning. Diagnostic files remain inside the
profile volume, are excluded from normal exports, and must be reviewed through a
redacted preview before the user shares them.

Commitarium does not offer raw transcript export through its normal session API.
A future explicit native-transcript export may be added as a privileged local
operation with a warning and best-effort redaction preview; the original export
must still be treated as sensitive.

## Verification requirements

Before a real provider adapter is enabled, automated tests must prove that:

- Sentinel secrets never appear in the worker event spool, coordinator database,
  API responses, SSE streams, Forgejo-bound payloads, or ordinary service logs.
- Unknown and malformed provider frames fail closed without storing their raw
  contents.
- Redaction is idempotent and stable under event replay.
- Truncation occurs after secret removal and cannot expose a secret across chunk
  boundaries.
- Active and waiting sessions are exempt from native transcript cleanup.
- Purging native state makes resume unavailable without deleting the coordinator
  or Forgejo records.

Tests may inspect the isolated provider-state fixture to prove native resume,
but must use synthetic credentials and delete the fixture afterward.

## Consequences

### Positive

- Provider resume remains available without turning native transcripts into a
  public or duplicated application record.
- SQLite, SSE, logs, and Forgejo share one safe normalized representation.
- Users can distinguish activity history, durable review history, and sensitive
  provider state.
- Fail-closed filtering makes redaction failures observable without leaking the
  rejected content.
- Retention rules protect paused and recoverable work from provider cleanup.

### Negative

- Sensitive plaintext still exists in the private provider-state volume while
  native resume is available.
- Two filtering stages and bounded event schemas add worker and coordinator
  implementation work.
- Pattern-based detection cannot guarantee removal of unknown secrets.
- Provider-specific cleanup behavior must be tested and monitored.
- Keeping redacted coordinator history for the project lifetime requires future
  storage-management and purge features.

## Alternatives considered

### Copy the full provider transcript into SQLite

This would simplify viewing and backup, but it would duplicate sensitive data,
couple coordinator schemas to provider formats, and broaden access to native
conversation state. It was rejected in favor of normalized activity.

### Disable provider transcript persistence

This minimizes local plaintext but prevents or weakens native session resume.
It conflicts with restart-safe recovery and was rejected for active real-worker
profiles.

### Redact native transcript files in place

This could reduce plaintext exposure but risks corrupting provider-owned state
and changing future conversational context. It was rejected; native state is
isolated and expired instead.

### Redact only in the coordinator

The coordinator intentionally does not receive secret values, so it cannot
reliably remove exact materialized secrets. It would also leave unredacted data
in the worker spool. Redaction therefore begins in the worker and is repeated at
the coordinator boundary.

### Treat summarization as redaction

A summary can still reproduce a credential or sensitive source fragment and may
omit facts required for audit. Summarization and redaction remain separate,
ordered operations: content is redacted before any persisted or published
summary is produced.

## References

- [OpenAI Codex advanced configuration](https://learn.chatgpt.com/docs/config-file/config-advanced)
- [OpenAI Codex troubleshooting and transcript locations](https://learn.chatgpt.com/docs/reference/troubleshooting)
- [Claude Code session management](https://code.claude.com/docs/en/sessions)
- [Claude Code application data](https://code.claude.com/docs/en/claude-directory)

## Reconsideration criteria

Reconsider native transcript retention if providers offer resumable sessions
whose local state contains no sensitive transcript, or a supported encrypted
session store with equivalent resume semantics.

Reconsider project-lifetime coordinator retention when project deletion,
storage quotas, regulatory requirements, or multi-user access controls become
implementation priorities.
