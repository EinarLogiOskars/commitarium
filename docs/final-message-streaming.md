# Final-message streaming

## Goal

Stream only the visible prose of an agent's final turn response into the
Commitarium transcript. Existing commentary, tool activity, file changes, and
reasoning summaries keep their current completed-event behavior.

The completed session event remains authoritative. A preview is advisory UI
state: it is never stored, replayed, or used to advance workflow state.

## Why this is a separate stream frame

Worker events and coordinator session events are durable. Their SSE `id` values
are consecutive replay cursors, and the corresponding event is persisted before
publication. Treating a token delta as an ordinary event while skipping storage
would create sequence gaps and break reconnect validation.

Final-message previews therefore share the existing SSE connections but not the
durable event model:

- durable frames have an SSE `id`, are persisted, and advance `Last-Event-ID`;
- preview frames use `event: message_preview`, have no SSE `id`, and are not
  replayed;
- a preview contains the complete visible prefix generated so far, rather than
  an append-only delta;
- the final durable event carries the same `stream_id` and replaces the preview.

## Wire format

Worker to coordinator:

```text
event: message_preview
data: {"session_id":"ses_123","attempt_id":"att_456","stream_id":"item_789","text":"The response so far"}

```

Coordinator to desktop:

```text
event: message_preview
data: {"stream_id":"item_789","text":"The response so far"}

```

There is deliberately no `id:` line. A client must update its durable resume
cursor only when a frame includes an ID.

The correlated durable event retains its normal event name and ID and adds an
optional `stream_id` field:

```text
id: sev_123
event: message
data: {"id":"sev_123","sequence":8,"type":"message","stream_id":"item_789","text":"The complete response","occurred_at":"..."}

```

## Provider behavior

Both providers retain their strict structured-output contracts. Their generated
text is accumulated, and a contract-aware extractor reads one top-level prose
field from the incomplete JSON:

| Output contract | Prose field |
| --- | --- |
| `goal_clarification` | `message` |
| `planning_lead` | `content` |
| `implementation_lead` | `summary` |
| `implementation_review` | `summary` |
| `implementation_readiness` | `summary` |
| `intervention` | `response` |
| `toolchain_setup` | `message` |

Extraction is best-effort for previews and strict for the completed response.
On each flush, the extractor scans the complete buffer received so far. It must
recognize a top-level field and JSON string escapes; it must not use a raw
substring search. An incomplete escape or UTF-16 surrogate is withheld until a
later snapshot makes it decodable.

Codex App Server supplies `item/agentMessage/delta` with `threadId`, `turnId`,
`itemId`, and `delta`. The item ID is the preview `stream_id`.

Claude Code must run with `--include-partial-messages` in addition to
`--output-format stream-json`. Its `stream_event` records are accumulated for
the final structured response. A stable response-local stream ID is synthesized
when the provider does not supply one.

Adapters coalesce updates before publication. The target cadence is 50 ms; an
unchanged or empty extracted prefix is not emitted.

## Safety and lifecycle invariants

- Preview text crosses the worker's normalization boundary before publication.
- Preview and final text are public agent prose, never hidden reasoning.
- A preview is scoped to one session and worker attempt.
- A stream ID is non-empty and stable for the response.
- Durable event validation, sequencing, persistence, and replay are unchanged.
- Losing any preview frame is harmless because the next preview is cumulative.
- Reconnecting may temporarily show no preview; the durable final event remains
  guaranteed by the existing replay path.
- Backpressure may drop previews, but must not make the worker attempt
  indeterminate or interfere with durable events.

## Backend slices

- [ ] Slice 1: provider-neutral preview contract and partial-JSON prose extractor.
- [ ] Slice 2: transient frames on worker SSE, including client decoding.
- [ ] Slice 3: relay previews through ingestion and coordinator session SSE.
- [ ] Slice 4: Codex and Claude adapter emission with coalescing.
- [ ] Slice 5: end-to-end validation, documentation, and frontend handoff.

## Frontend handoff

The backend portion is complete when the session SSE route emits the two frame
classes described above. Frontend work should then:

1. Replace transcript polling with `streamEvents` subscriptions to
   `/api/v1/sessions/{id}/events/stream`. A view containing multiple agent
   sessions subscribes to each and retains the session's role alongside frames.
2. Seed durable history once with `getSessionEvents`, then deduplicate subsequent
   durable frames by event ID. The shared SSE helper already retains the last ID
   only when a frame supplies one, which is correct for previews.
3. Keep previews in a map keyed by session ID plus `stream_id`. Each
   `message_preview` replaces the map entry's text; it is not appended.
4. Render a preview as a normal agent message bubble with a streaming indicator.
5. When a durable event with the same `stream_id` arrives, remove the preview and
   render the durable event. The durable text always wins.
6. Clear previews when leaving a live phase. Do not place previews in historical
   phase windows or persist them in frontend state.
7. Preserve scroll pinning: follow preview growth only when the transcript was
   already pinned near its bottom.
8. Respect `prefers-reduced-motion`; the text still updates, but a caret or pulse
   should be disabled.

The frontend must continue polling or otherwise refreshing run/session status
until those state changes have their own live feed. This feature replaces only
transcript-event polling.
