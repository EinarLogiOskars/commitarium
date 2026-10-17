# Login Recovery: Implementation Plan

## Problem

On 2026-10-14 three assistant turns failed in fifteen minutes with Claude's
"OAuth session expired and could not be refreshed". The saved login was fine:
the next turn refreshed it. Commitarium treated the failure as final:

- The assistant said "must log in again on the Agents page" and dropped the
  user's message; nothing ever sent it again.
- The Agents page kept saying "Provider login verified" (it reads the stored
  credentials, not whether they work).

An audit of every place an agent turn runs found more of the same:

| Where | Today on a login failure | Comes back by itself? |
| --- | --- | --- |
| Work order turns (plan, implement, review, update onto main, preview fixes) | Pause with a login wait | Only when a sign-in finishes **inside the app** (`POST /agents/{id}/logged-in`) |
| Sessions | Pause with a login wait; no sign-in button, only Continue | Same |
| Assistant messages | "Must log in again"; the message is dropped | No |
| Assistant wake-ups (work order events, autopilot, gaps) | Pending events were cleared before the turn; lost | No; autopilot can stall |
| Vision, clarification, stack setup chats | A generic "did not complete"; login never mentioned | No; New app waits at the vision step forever |

Also: a login failure is never retried, though some are transient; a run
with two agents signed out remembers only one; a paused run loses its
login wait when the sign-in arrives; autopilot may press Continue on a run
that waits for a sign-in, which fails again.

## Principles

- **Retry before blaming the login.** A login failure gets the interrupted
  turn's backoff (30 s, 2 min, 5 min) first; a transient one fixes itself
  unseen.
- **One record of who needs a sign-in.** After the retries, the agent is
  marked as needing a sign-in, with what waits on it. Every kind of turn
  waits on that one record instead of failing in its own way.
- **Nothing is dropped.** A message, a wake-up, a vision turn waits and runs
  once the agent works again.
- **Resume however the login came back.** A sign-in inside the app resumes
  everything at once; the coordinator also tries again every few minutes, so
  a login fixed outside the app, or one that recovered by itself, resumes
  too.
- **The user hears it once, in plain words, with the button there**
  (non-technical users): "Claude needs you to sign in again. Your message
  is kept and will be sent after." Autopilot never spends recoveries on it.

## Status

L1–L4 and L6 are built (ADR-034). L5, the desktop's sign-in in place, is
next. L4 is thinner than planned: the vision, clarification, and setup chats
say to sign in and record the agent, but the user starts them again; New
app's launch does restart its vision by itself.

## Slices

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| L1 | `fix(orchestration): retry a login failure before asking to sign in` | A work order or session turn that fails on its login is retried with the interrupted turn's backoff before it records a login wait. | — |
| L2 | `feat(coordinator): agents that need a sign-in` | Per-agent sign-in state (since, last error), set when a login failure outlasts the retries, cleared by the desktop's `logged-in` or by a turn on that agent that succeeds. Login waits keep one row per run and role; a paused run keeps its wait. Every few minutes the coordinator retries the oldest waiting turn per agent; success clears the state and resumes everything waiting. `GET /agents` reports `needs_sign_in` and what waits. Autopilot's continue leaves login waits alone. | L1 |
| L3 | `feat(assistant): keep messages and wake-ups through a sign-in` | An assistant turn that fails on its login keeps its message (or its pending events), marks the conversation as waiting for the agent, and runs again when the agent works. The panel says so in plain words. | L2 |
| L4 | `feat: vision, clarification, and setup chats wait for a sign-in` | The same for the vision, clarification, and stack-setup chats; New app shows the wait instead of stalling at the vision step. | L2 |
| L5 | `feat(desktop): sign in where it is needed` | The assistant panel, sessions, and New app show "<agent> needs you to sign in again" with **Sign in** in place; the Agents page shows "Needs sign-in" and what waits instead of "login verified". | L2–L4 |
| L6 | `docs: record login recovery` | ADR, coordinator API, and this plan updated. | L1–L5 |

## Not in this plan

- Probing logins with a dedicated test call: retrying the waiting work is
  the probe, and costs nothing when the login is still gone (the turn fails
  before any tokens).
- Refreshing provider tokens ourselves: the CLIs own their credentials.
