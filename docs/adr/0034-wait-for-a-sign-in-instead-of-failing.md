# ADR-034: Wait for a sign-in instead of failing

- Status: Accepted (amends [ADR-017](0017-pause-work-orders-instead-of-failing.md))
- Date: 2026-10-17

## Context

On 2026-10-14 three assistant turns failed in fifteen minutes with Claude's
"OAuth session expired and could not be refreshed", though the saved login
was fine: the next turn refreshed it. The assistant dropped the user's
message and said to log in again; the Agents page still said the login was
verified. An audit of every place an agent runs (LOGIN_RECOVERY_PLAN.md)
found work orders resumed only after a sign-in inside the app, assistant
messages and wake-ups dropped, the vision and setup chats silent about the
login, and a new app's launch stalled at its vision for good.

## Decision

A login failure is waited out, never final:

- **Retry first.** A login failure takes the interrupted turn's backoff
  (30 s, 2 min, 5 min), in work orders and in the assistant alike.
- **One record per agent.** Past the retries, the agent is recorded as
  needing a sign-in (`agent_sign_ins`); its later login failures wait at
  once. Work orders keep a login wait per run and role, the assistant keeps
  the turn's prompt, and both wait on that record.
- **Resume however the login came back.** A sign-in in the desktop, any turn
  on the agent that works, or the coordinator's own try of the
  longest-waiting work (every 5 minutes, every 30 after an hour) clears the
  record and resumes everything waiting.
- **Nothing is dropped.** The assistant keeps the message or the wake-up;
  events and new messages ride along. A new app's launch whose vision writer
  stopped wakes the assistant to write it again.
- **Plain words.** The user hears once that the agent needs a sign-in and
  that the message is kept; autopilot spends no recovery on it.

## Consequences

- A transient login failure fixes itself unseen; a real one waits until the
  user signs in, then everything continues.
- The coordinator tries waiting work now and then; a try on a login that is
  still gone fails before any tokens.
- The vision, clarification, and setup chats still end their turn on a login
  failure; they say so plainly and record the agent, and the user starts
  them again after signing in.
