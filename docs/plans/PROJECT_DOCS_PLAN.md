# Project Docs: Design Note

**Status:** proposed 2026-11-19, [ADR-042](../adr/0042-projects-keep-their-own-decisions.md);
planned for 0.16.0 as one small PR next to actions as tools. Not built.

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
project docs. The only doc an agent is asked to write is the README, by the
new-project setup (`internal/httpapi/project_setup.go`) and the preview
setup (`internal/httpapi/preview_setup.go`). Vision reads the README and the
file tree (`internal/vision/vision.go`).

## Decision

Agents keep three kinds of docs in each project's repository, and the user
never has to know they exist.

1. **Decision records** for lasting choices only: a new dependency or
   service, the data model, auth, the deploy shape, an API contract other
   code relies on, or reversing an earlier record. Most work orders write
   none, so a small project ends up with a handful at most. Each one is
   short: context, decision, consequences, and which record it replaces.
   They live in `docs/adr/`, numbered, unless the project already keeps
   decision records somewhere else, in which case the agents use that place
   and its format.
2. **README** kept true: what the app is, how to run it, how to test it, and
   the settings it needs. A work order that changes any of those updates it.
3. **ARCHITECTURE.md**, a short map of the main parts and how they connect,
   once a project has more than one part worth naming (a frontend and an
   API, a worker, a second service). Kept to about a page; updated when a
   part is added or removed.

The briefs say when
a record is worth writing and what it is for, not a template to fill in.

## Where it goes in the briefs

All changes are instruction text; no storage, API, or UI.

| Brief (`prompts.go`) | Addition |
| --- | --- |
| `planningInstructions`, `soloPlanningInstructions` | Read the list of decision records and open the ones the goal touches. When the plan makes a lasting choice, include writing its record in the step that makes the choice. Say in the plan when the goal goes against an accepted record, and why. |
| `reviewerPlanningInstructions` | Check the plan against the accepted records; a contradiction with no replacing record is a numbered concern. |
| `implementationInstructions`, `planlessImplementationInstructions` | Keep the README true when run, test, or settings change; keep ARCHITECTURE.md true when a part is added or removed; write a record the plan called for, or one for a lasting choice made during the work. A quick change writes one too if it makes a lasting choice. |
| `implementationReviewInstructions` (first pass) | A change that contradicts an accepted record without replacing it, or that changes how to run the app without updating the README, is a finding. A missing ARCHITECTURE.md update is a non-blocking note. |

The preview-setup and new-project briefs already ask for the README; they
stay as they are.

**Mayka:** her vision turns already get the README and the file tree, so
record filenames show up there. One line in the vision instructions: the
files under the project's decision-record folder are decisions already
made; when the user asks for something that goes against one, say so in
plain words and treat it as a change to that decision. No reading of
record contents by Mayka in this slice.

## Not doing

- No Decisions view in the app. A plain-words list for the user may come
  later, built on these files.
- No checks in code that a record was written. The reviewer judges.
- No RFCs, changelogs (events cover them) or runbooks.
- No migration for existing projects, since there are no users yet: their
  next work orders start keeping the docs.

## Build

One PR:

1. The brief additions above, with `prompts_test.go` and
   `prompts_review_test.go` in `internal/orchestration` updated.
2. The vision instruction line and its test.
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
