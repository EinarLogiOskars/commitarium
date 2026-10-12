# ADR-020: Keep the project vision as a Commitarium record

- Status: Accepted
- Date: 2026-10-11

## Context

The 1.0 vision has an assistant that turns what the user wants into work
orders. To do that it needs to know where the project is going and what
matters first. Until now that knowledge lived only in the user's head and in
each work order's description.

A common way to keep it is a PRIORITY.md file in the repository. That would put
a planning document in the user's product, mix it with the code's history, and
make linking plan items to work orders depend on parsing Markdown.

## Decision

Each project has one **vision**: a short statement of why the project exists
and what it should become, and a prioritized plan below it. Commitarium keeps
it as its own record, outside the repository, and shows it on the project's
Vision page.

- **The plan.** Priorities run from P0 (needed first) to P9. Items nest at most
  three levels deep. Numbers such as P0.2.1 come from position; each item has
  a stable ID, so reordering or rewording never breaks a link. Only an item
  without sub-items starts a work order, and it starts exactly one. A group is
  done when everything under it is.
- **The assistant writes it.** The user either describes the app ("Write it
  from my description", one turn) or lets the assistant ask what it needs to
  know first ("Talk it through"). Both use the structured `vision_plan` result
  in a consultant conversation with a read-only checkout of the default
  branch, so it plans from what the code already does. Items the project
  already delivers are kept in the plan and marked **done**, so no work order
  repeats them; the user can change that mark when editing. The result carries a **confidence** (high, medium, or low)
  and the **guesses** the assistant had to make. The page warns when confidence
  is low, and "Answer these" continues as a conversation about the guesses.
- **The user owns it.** The user can edit the statement and items, add or
  remove items, or ask the assistant to rewrite the whole vision. A rewrite
  keeps the work orders of items it keeps by ID. Saves are guarded by a
  revision, so an edit never silently overwrites a newer version.

- **New projects start from it.** Creating a project with a description of the
  app starts the assistant at once. For a repository without code it also
  recommends the stack: runtime presets, the frameworks, and why. The user
  confirms or changes it, and "Set the project up" starts one visible quick
  change that scaffolds a minimal app on that stack with a test and the
  preview's compose file. The plan then holds only the app's own work.

## Consequences

- The vision is planning input for the assistant and the backlog to come; it
  is not part of the product's code or history.
- A copy in the repository can be added later as an optional export, without
  changing where the vision is kept.
- The vision is stored as files next to the project's stack records in the
  coordinator's data directory.
