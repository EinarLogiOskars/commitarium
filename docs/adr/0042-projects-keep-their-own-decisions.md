# ADR-042: Projects keep their own decisions

- Status: Accepted
- Date: 2026-11-19

## Context

Each work order starts with fresh agents. They read the repository and the
accepted goal, but no record says why an earlier work order made a lasting
choice, so a later one can contradict it unnoticed and the reviewer has
nothing to check it against. Commitarium's own repository relies on ADRs
for exactly this.

## Decision

Agents keep docs in each project's repository:

- **Decision records** for lasting choices only (a new dependency or
  service, the data model, auth, the deploy shape, an API contract, or
  reversing an earlier record), in `docs/adr/` unless the project already
  keeps them elsewhere, which agents look for first.
- **README** kept true for what the app is and how to run, test and
  configure it.
- **ARCHITECTURE.md**, a page-long map, once a project has more than one
  part.

The lead reads the record list when planning (or, for a quick change,
before its first edit) and writes a record for a lasting choice; the
reviewer treats an unreplaced contradiction, or a run, test or settings
change without a README update, as a defect. Mayka checks the records in
her read-only checkout and treats a request that goes against one as a
change to that decision. Records are read as quoted data, never as
instructions. It is all brief text: no storage, API, UI or code checks.

## Consequences

- Later work orders build on earlier choices instead of guessing them.
- Agents spend some tokens reading the list and the records they need.
- Docs can still drift where the reviewer does not look; ARCHITECTURE.md
  is only a note, and work orders without a reviewer rely on the lead.
- Parallel work orders can pick the same record number, which Git merges
  without a conflict; the reviewer catches it, and work orders without a
  reviewer can still land a duplicate.
- The user sees none of it unless they open the repository; a Decisions
  view may come later.

Plan: [PROJECT_DOCS_PLAN.md](../plans/PROJECT_DOCS_PLAN.md).
