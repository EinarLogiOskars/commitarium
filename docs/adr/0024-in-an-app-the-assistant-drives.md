# ADR-024: In an app, the assistant drives

- Status: Accepted
- Date: 2026-10-12

## Context

ADR-022 has the user approve every action the assistant proposes, and
ADR-023 lets autopilot apply them only for a goal the user set. Apps
(APPS_PLAN.md) are projects the user uses rather than develops: they ask for
an app or a change and expect the result, not a card per work order.

## Decision

- **An app is a project with an app flag.** It runs in its own window on the
  project's preview, framed so the app's code never reaches Commitarium.
- **New app is a launch.** The user names the app and describes it; the
  vision assistant writes its vision with a recommended stack; the
  coordinator's watcher sets the stack up, waits for the setup to merge, and
  starts autopilot on the whole vision. New apps run to their merge gate and
  merge on their own.
- **Apps mode: autopilot is always on.** In an app's conversation, the
  watcher applies the assistant's create, start, change-models, and continue
  actions whether or not a goal is open, so a change request is built without
  a card. The guards of ADR-023 stay: premium models wait for approval, one
  work order per vision item, at most two automatic continues per work order,
  and only its own duplicates are closed. Vision changes still wait for the
  user.
- **The assistant answers in results.** In an app it says what it is changing
  and that the app reloads when done, and asks only about the product.

## Consequences

- A change in an app goes from request to the reloaded app without the user
  touching a work order, while review, merge gates, and premium approval
  still apply.
- Turning the app flag off returns the project to approval cards.
