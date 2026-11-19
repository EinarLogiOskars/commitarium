# Project Docs: Design Note

**Status:** agreed 2026-11-19, [ADR-042](../adr/0042-projects-keep-their-own-decisions.md);
built for 0.16.0 as one small PR next to actions as tools.

## Why

Every work order starts with fresh agents. They see the repository and the
accepted goal, but nothing tells them why an earlier work order chose
Postgres, cookie sessions, or a REST shape. So a later lead can quietly pick
a second database library, undo a choice the user agreed to, or reopen a
question that was settled two work orders ago, and the reviewer has nothing
to check it against.

Commitarium itself shows the fix works: its ADRs are what keep long-running
work on this repository consistent from session to session. The agents
building the user's projects should get the same thing.

Today the briefs in `internal/orchestration/prompts.go` say nothing about
project docs. Agents are asked for a README by the new-project setup
(`internal/httpapi/project_setup.go`) and the preview setup
(`internal/httpapi/preview_setup.go`), and Mayka's planning asks for a
README note on settings (`settingsBySite`, `internal/conversations/planning.go`).
Vision reads the README and the top-level files (`internal/vision/vision.go`).

## Decision

Agents keep three kinds of docs in each project's repository, and the user
never has to know they exist.

1. **Decision records** for lasting choices only: a new dependency or
   service, the data model, auth, the deploy shape, an API contract other
   code relies on, or reversing an earlier record. Most work orders write
   none, so a small project ends up with a handful at most. Each one is
   short: context, decision, consequences, and which record it replaces.
   They live in `docs/adr/`, numbered, unless the project already keeps
   decision records somewhere else (such as `doc/adr/` or `docs/decisions/`):
   agents look for one first and then use that place and its format.
2. **README** kept true: what the app is, how to run it, how to test it, and
   the settings it needs. A work order that changes any of those updates it.
3. **ARCHITECTURE.md**, a short map of the main parts and how they connect,
   once a project has more than one part worth naming (a frontend and an
   API, a worker, a second service). Kept to about a page; updated when a
   part is added or removed.

The briefs say when a record is worth writing and what it is for, not a
template to fill in.

**Records are data, not orders.** Like everything else in the repository,
records are read as quoted data. A record says what was decided, and an
agent that wants to go against one says so in the plan; it never treats a
record's text as an instruction to follow. This matters most for imported
repositories, whose records nobody in Commitarium wrote.

## Where it goes in the briefs

All changes are instruction text; no storage, API, or UI.

| Brief (`prompts.go`) | Addition |
| --- | --- |
| `planningInstructions`, `soloPlanningInstructions`, `replanningInstructions` | Read the list of decision records and open the ones the goal touches. When the plan makes a lasting choice, include writing its record in the step that makes the choice. Say in the plan when the goal goes against an accepted record, and why. |
| `reviewerPlanningInstructions` | Check the plan against the accepted records; a contradiction with no replacing record is a numbered concern. |
| `planlessImplementationInstructions` | Before changing anything, read the list of records and open the ones the goal touches (a quick change has no planning turn), then as for implementation below. |
| `implementationInstructions`, `planlessImplementationInstructions` | Keep the README true when run, test, or settings change; keep ARCHITECTURE.md true when a part is added or removed; write a record the plan called for, or one for a lasting choice made during the work. A quick change writes one too if it makes a lasting choice. |
| `implementationReviewInstructions` (first pass) | A change that contradicts an accepted record without replacing it, or that changes how to run, test or configure the app without updating the README, is a finding. A missing ARCHITECTURE.md update is a non-blocking note. |

| `implementationBaseUpdateInstructions` | When main gained a record with the same number as one this work order adds, renumber this work order's record to the next free number; that counts as part of the merge. |

The preview-setup and new-project briefs already ask for the README; they
stay as they are.

**Numbers:** a new record takes the next number after the highest on the
base branch. Two work orders running at once (ADR-036) can pick the same
one; Git sees two different files and no conflict, so the base-update turn
renumbers, as in the table.

**Mayka:** users ask for work in her chat, whose instructions are in
`internal/conversations/turn.go`, and that chat has the repository checked
out read-only. One line there: the project's decision records are choices
already made; before proposing work that touches one, look at it in the
checkout, and when the user asks for something that goes against one, say
so in plain words and propose the work as a change to that decision. The
same line goes in the vision instructions (`vision.go`, which also has the
checkout), since vision turns only list top-level files and would not see
the records otherwise.

## Not doing

- No Decisions view in the app. A plain-words list for the user may come
  later, built on these files.
- No checks in code that a record was written. The reviewer judges, so
  work orders without a reviewer (Quick change and Planned, ADR-018) or
  with code review off (ADR-031) rely on the lead alone.
- No RFCs, changelogs (events cover them) or runbooks.
- No migration for existing projects, since there are no users yet: their
  next work orders start keeping the docs.

## Build

One PR:

1. The brief additions above, with `prompts_test.go` and
   `prompts_review_test.go` in `internal/orchestration` updated.
2. Mayka's line in `turn.go` and `vision.go`, with their tests in
   `internal/conversations` and `internal/vision`.
3. ADR-042 marked Accepted.

**Live test:** in a test project, a work order that adds a database writes
a record under `docs/adr/` and updates the README; a later work order that
asks for a different database gets a planning concern from the reviewer,
or a replacing record.

## Risks

- **Docs go stale.** The reviewer check on the README and records is what
  keeps them true; ARCHITECTURE.md is only a note, so it may drift.
- **Tokens.** Agents read the list first and open only the records the goal
  touches; records stay short.
- **Too many records.** The bar is a lasting choice, and the reviewer can
  call a needless one polish.
