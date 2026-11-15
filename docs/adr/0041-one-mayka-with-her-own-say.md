# ADR-041: One Mayka with her own say

- Status: Accepted; amends ADR-022, ADR-023, ADR-024, ADR-027 and ADR-035
- Date: 2026-11-15

## Context

Mayka runs two ways, chosen by `projects.assistant_mode` (ADR-027). In
*Assists me* every action she proposes is a card. In *Builds everything* the
same proposals become cards that the coordinator approves for her, by a
rule set per action type that the desktop repeats. A checklist tells her
the next step, and she must start an autopilot goal to keep building, which
then cannot finish when the user skips an item.

The workflows (vision, work orders, agents, review, merge gate, preview
check) are good and stay. How she is driven through them is the problem:
she proposes and a rule set decides, she follows a script rather than a
goal, and the mode branches through about 20 places.

## Decision

**One Mayka.** There are no modes. In every project she decides, action by
action, whether to **take** it (applied right after her turn) or **ask** for
it (a card). When unsure, she asks.

**Her say.** Each project keeps a note, *how much I do here*: a level the
UI shows (*Ask first* or *Build everything*) and her own words for the
rest ("advise only", "deploy after each change", "skip P1.3"). She records
what the user tells her in chat; the user can also change the level in
Project settings. Unset means *Ask first*.

**Hard limits are the coordinator's.** One list, enforced when an action is
applied, whatever her say is: premium models; the user's own work orders
unless the user asked in this chat; removals of work she did not create;
the server cards (Homelab, sign-in, app access, the apps page, and deploys
that move or drop an address); and the loop bounds (16 running work orders,
8 automatic continues per work order, 2 refusal wakes in a row). A deploy
to an existing address happens only when the user asked, in the chat or in
her say; otherwise she says the work merged but is not live yet.

**Wakes say what changed.** No checklist of next steps. Missing pieces are
facts in her state. Under *Build everything* she gets one idle wake when
nothing runs and the vision has open items, repeated only after something
changes. A taken action that fails wakes her with the outcome at once.

**Autopilot is a goal the user hands her.** She never starts one herself.
She can drop an item the user said to skip, so the goal can finish. The
desktop shows autopilot only while such a goal is on.

## Consequences

- One rule set, in the backend; the desktop shows each action's outcome
  instead of re-deriving it.
- `assistant_mode` carries the say's level (`builds` is *Build everything*,
  `assists` is *Ask first*); no migration beyond that, since there are no
  users yet.
- She may now take steps in an *Ask first* project that used to be cards,
  such as a brief, when the conversation clearly asks for them. The user's
  approval for the hard limits is unchanged.
- Workspaces and actions as tools in her turn follow, in the
  [One Mayka plan](../plans/ONE_MAYKA_PLAN.md).
