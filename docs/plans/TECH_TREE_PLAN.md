# Work as a Tech Tree: Implementation Plan

**Status:** proposal written 2026-11-07 from what was agreed on 2026-11-05;
not started. Needs the user's go-ahead.

The Work graph should read like a game's tech tree: each work order sits
under what it builds on, and a line means "needs that first", nothing else.
Today a line also means "ran after", and the P bands cut across the graph.
That confused the Notes dry run. P0.3 (Markdown) waited behind the slow editor
and finished after all the P1 work. It held back the "usable" gate and was
drawn above the P1 line.

## What was decided (2026-11-05)

- **Data:** each work order gets `builds_on`, a list of work order IDs.
  Mayka fills it in when she turns the plan into work orders. Today's single
  `after` on her start and create actions becomes a list of one. Autopilot
  starts a work order only once everything it builds on has merged (or closed).
- **Graph:**
  - A card's row is its dependency depth.
  - Lines follow only `builds_on`; the "every card of the row before" fallback
    goes.
  - Work that builds on nothing hangs off "Set the project up".
  - The horizontal P band lines go. Cards keep their P label, with at most a
    faint tint per level.
  - Time shows through status. Animate still replays in the real run order.
- **Scheduling:** when P0 and P1 compete, P0 goes first, both for a free
  slot and in the merge queue.

## Where things are today

| Piece | Today | File |
|---|---|---|
| What a start waits for | `Action.After`: one ID or title, kept on Mayka's proposed action, not on the work order | `internal/featureartifact/assistant_turn.go`, `internal/conversations/goal.go` `waitsFor` |
| What the graph draws | `queued_after`: only for proposed, queued starts | `internal/httpapi/features.go` `QueuedStarts` |
| Rows | Columns from when work really ran | `desktop/src/views/graphLayout.ts` `graphColumns` |
| Lines | `after`, otherwise every card of the row before | `graphLayout.ts` `graphEdges` |
| Merge order | `ORDER BY joined_at` | `internal/database/merge_queue_store.go` |
| Free slots | First held start that asks, up to `SafetyCap` | `goal.go` `ImplementationHold` |

The gap is that `after` dies with the action. Once a work order runs, nothing
records what it builds on, so the graph falls back to time.

## Steps

### T1: `builds_on` on the work order
- A `builds_on` column on features: a JSON list of work order IDs, empty by
  default, in the API as `builds_on: string[]`.
- Mayka's `create_work_order` and `start_work_order` take `builds_on`, a list
  of IDs or titles of her other actions, in place of `after`. The schema
  description keeps today's rule: only work whose code or result this one
  needs, never just the same files. Titles resolve to IDs when the action is
  applied, as `waitsFor` does today, and are stored as IDs.
- `waitsFor` holds until every entry has merged or closed. A work order it
  cannot find does not hold work back, as today.
- The vision-to-work-orders turn already orders the plan. Its instructions
  add one line: fill `builds_on` from the plan's order where an item really
  needs another.
- Work orders made by hand start with an empty list. Editing it in the
  inspector is a later step, only if wanted.
- No migration for old projects, since there are no users yet: they draw as roots.

### T2: The graph is a tree
- `graphColumns` becomes `graphRows`: a work order's row is
  1 + the deepest row of what it builds on. Roots sit on row 1 under a
  "Set the project up" card.
- `graphEdges` draws only `builds_on`. The fallback goes.
- P band lines go. Each card keeps its P label; a faint left border per
  level is optional, decided when it is on screen.
- Within a row, order by P level, then plan position, so P0 work sits left.
- Animate keeps replaying by real start and end times. Cards appear in the
  order they ran, in their tree positions.
- The interim fix from 2026-11-05 (queued work stays in its band) goes with
  the bands.

### T3: P0 first
- **Free slots:** when a slot frees and several held starts are ready,
  `ImplementationHold` lets the lowest P level go first, then the oldest.
  The P level comes from the vision item the work order is linked to; work
  with no item goes last.
- **Merge queue:** order by P level, then `joined_at`. A merge already under
  way is never pre-empted, so the order changes only who goes next.

## Tests
- Go: `waitsFor` with two `builds_on` entries, one merged and one running,
  holds; with both merged it starts. A title resolves to an ID once applied.
- Go: slot and merge order put P0 before an older P1.
- TS: `graphRows` depth for a diamond (A to B and C, both to D) puts D on
  row 3 with two lines; a root sits under "Set the project up"; no line
  without `builds_on`.
- Live: re-run the Notes dry run. Markdown sits under what it needs and
  merges before the P1 work.

## Open questions for the user
1. Should a person be able to edit `builds_on` in the inspector, or is it
   Mayka's alone for now? The proposal is Mayka's alone.
2. Should there be a faint tint per P level on cards, or only the label?
