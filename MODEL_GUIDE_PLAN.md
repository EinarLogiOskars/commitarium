# Model Guide and Review Steps: Implementation Plan

## Problem

The assistant chooses every model of the work it creates (ADR-027), guided by
one line: "a lighter, cheaper one for small or mechanical changes, a stronger
one for hard design or debugging." In practice it uses the same strong pair
for everything. That is fine on a Max plan and burns a $20 plan's limits in a
few work orders.

The local run history (142 runs: 37 Full, 73 Planned, 32 Quick) shows where
the usage goes and what it buys:

- **Plan review is where review pays.** Across 35 Full runs it raised about
  120 findings that would plausibly have caused defects: totals 100× too
  large, double-counted VAT, writes joining a SQLite transaction, a container
  that could never migrate, a customs gate any receipt satisfied. Nearly every
  non-trivial plan got some, from every lead model.
- **Code review mostly confirms.** 91% of reviewed runs were approved the
  first time, with about five real defects in total, mostly races and
  merge-time bugs. It approved a Dockerfile that did not build because the
  reviewer could not run Docker.
- **Independent acceptance tests caught nothing.** About 124 tests across 20
  runs found zero implementation defects. At least 13 failures were bugs in
  the reviewer's own tests and two were environment problems. Every real
  defect after implementation came from targeted reasoning or a purpose-built
  reproduction, not from the pre-written suite.
- **Where a Full run's tokens go** (24 runs with usage recorded): planning and
  plan review about 7% of input and 20% of output; implementation about 42%
  and 36%; everything after implementation (acceptance tests, code review,
  correction, readiness) about 35% and 40%. The cheapest step finds most of
  the problems; the most expensive finds few.
- **Ceremony.** Full runs on a three-line Markdown file, a README and a
  one-line Dockerfile fix; Opus 5.5 leading eight Quick changes.
- **Sonnet 5.5 found as much in plan review as Opus 5.5** (about 10 and 8.6
  findings per run) on comparably hard work.
- **Effort is never chosen.** Commitarium passes no effort, so each model runs
  at its catalog default; `gpt-6.1-sol` defaults to `low`.
- **The work-order cost line shows tokens per role and phase, but not which
  models spent them**, so none of this is visible in the app.

Public evidence agrees: mid-tier models are close to the top for code review
and implementation, the top tier matters most for planning and hard
reasoning, a reviewer rarely lifts a weaker implementer beyond its own
capability, and a strong plan lets a cheaper model implement (sources in
ADR-031).

## Principles

- **Tiers, not rankings.** A model is Premium, Frontier, Strong, Light, or
  Avoid. Rankings go stale with every release; a tier changes rarely.
- **Exact IDs only.** Quality is not monotonic by version (Opus 5 was a step
  down from 4.8) and OpenAI reuses tier names across generations
  (`gpt-5.6-sol`, `gpt-6-sol`, `gpt-6.1-sol`). Nothing is inferred from a
  family name or version number; a model not in the guide is unrated.
- **The official guide is the base; the user's changes are overrides.** Users
  never hold a copy that freezes. Untouched entries follow every release.
- **Spend strength where it pays.** Strong planning and plan review; cheaper
  implementation and a light code review once the plan is agreed.
- **Keep a step only if it catches things.** Plan review stays and becomes
  its own step; acceptance tests go; code review shrinks to one verification
  pass.
- **The coordinator only refuses** (ADR-027). The guide informs the assistant
  and the Start form's suggestion; the coordinator enforces Premium and the
  effort ceiling on the assistant's choices and nothing else. The user's own
  choices are never gated.
- **Measure.** Every token in the cost breakdown names its model and effort.

## The official guide

Shipped in the coordinator (`internal/modelguide/guide.json`, embedded), with
an `as_of` date, one entry per exact model ID: tier and a one-line note. It
is updated with releases. Draft contents:

| Model ID | Tier | Note |
| --- | --- | --- |
| `claude-fable-5-1` | Premium | Hardest design and debugging; approval first |
| `gpt-6-astra` | Premium | OpenAI's top tier; approval first |
| `claude-opus-5-5` | Frontier | Best planner and plan reviewer; tops Terminal-Bench 4.0 |
| `claude-opus-4-8` | Frontier | Previous Opus; still strong, prefer Opus 5.5 |
| `claude-sonnet-5-5` | Strong | Near Opus on implementation and review at lower cost |
| `gpt-6.1-sol` | Strong | Good value lead and reviewer; weaker on long terminal tasks |
| `claude-haiku-5-5` | Light | Mechanical changes only |
| `gpt-6-luna` | Light | Mechanical changes; decent cheap reviewer |
| `claude-opus-5` | Avoid | Superseded by Opus 5.5 |
| `claude-sonnet-5` | Avoid | Superseded by Sonnet 5.5 |
| `claude-haiku-4-5-20251001` | Avoid | Superseded by Haiku 5.5 |
| `gpt-6-sol` | Avoid | Superseded by 6.1 Sol at the same price |
| `gpt-5.6-sol` | Avoid | Superseded by 6.1 Sol: stronger at agentic coding, half the price |
| `gpt-5.6-terra`, `gpt-5.6-luna` | Avoid | Previous generation |

**Avoid** means the assistant does not choose it while another fitting model
is connected; the user can still pick it. **Unrated** models (not in the
guide, no override) need approval on the assistant's choices, like Premium,
until the user gives them a tier: a new model may be priced as a top tier.

## Rules

The assistant's instructions carry these rules; its state carries the merged
guide for the models its agents offer.

**Steps by kind of work** (tightens ADR-018's guidance):

| Kind of work | Steps |
| --- | --- |
| Docs, copy, config values, renames, one-line fixes | Quick. Never a reviewer. |
| Small, low-risk changes with an obvious shape | Planned |
| Real features: UI with state, APIs, domain logic, infra | Reviewed plan |
| Data, money, auth, migrations, concurrency | Full |

**Models and effort by phase.** A role can use one model and effort for
planning and another for the rest; the agent stays the same, so a phase change
stays within one provider (ADR-021).

| Work | Lead: plan | Lead: implement | Reviewer: plan review | Reviewer: code review |
| --- | --- | --- | --- | --- |
| Quick, mechanical | — | Light, low | — | — |
| Quick, touches logic | — | Strong, medium | — | — |
| Planned | Strong, high | Strong, medium; Light when every step is mechanical | — | — |
| Reviewed plan | Strong or Frontier, high | Strong, medium | Strong, high | — |
| Full | Strong or Frontier, high | Strong, medium | Strong, high; Frontier for the riskiest | Strong, medium |
| Hard design or debugging throughout | Frontier, high | Frontier, high | Frontier, high | Strong, medium |

- Lead and reviewer from different providers when both are connected
  (unchanged).
- A lead stuck after two correction rounds or a blocker: escalate one tier
  with `change_models`.
- Usage still decides between equal choices: the agent with more usage left
  or an earlier reset (unchanged).
- The assistant's own model is the user's setting (ADR-027). The settings page
  recommends a Frontier model: it writes the vision and every work order, and
  its turns are a rounding error next to the runs (14k output tokens against
  2.8M in the local history).

## User overrides

- The Agents page lists every model the connected agents offer, with its tier
  (official or the user's) and note. The user can set a tier, including Avoid,
  or reset to official.
- **"Ask before using" becomes the Premium tier.** One place to set it.
  `ask_before` is deprecated: a migration turns families the user added into
  Premium overrides for matching catalog models, and default families the user
  removed into Frontier overrides; the setting is then ignored.
- An override records the official tier it replaced. When a release changes
  the official tier of an overridden model, the Agents page shows one notice
  per model (the inbox lists only project work, so it stays out) ("The official guide now rates Opus 5 Strong; you have it
  as Frontier"), with Keep mine and Use official. Nothing else notifies.

## Effort

- Each turn carries an effort. Empty means the model's default (today's
  behavior). Codex already reports supported efforts in its catalog; the
  Claude adapter must report and pass them too.
- **Effort ceiling.** An assistant setting, "Highest effort the assistant may
  choose", default `high`. The coordinator refuses an assistant choice above
  it, with a reason the assistant can act on, like any choice that cannot run.
  The user's own choices are not limited.

## Cost breakdown

The work-order cost line splits by role, phase, model and effort ("Lead ·
Plan · Opus 5.5 · high · …"). Usage is already stored per attempt with its
model; effort is recorded alongside.

## Review steps

**The reviewer step splits into plan review and code review** (amends
ADR-018, where review is all or nothing). A run records each separately.
Code review requires plan review: a reviewer that did not agree the plan has
no baseline to check against.

| Preset | Clarification | Documented plan | Plan review | Code review |
| --- | --- | --- | --- | --- |
| Quick change | off | off | off | off |
| Planned | on | on | off | off |
| Reviewed plan (new) | on | on | on | off |
| Full | on | on | on | on |

Plan review keeps everything it does today: numbered concerns, the lead's
answers, and the reviewer's confirmation of the submitted plan.

**Independent acceptance tests are removed.** New runs never author them; the
toggle leaves the Start form, the assistant's actions, and the API (accepted
and ignored, like ADR-027's deprecated project fields). Runs already started
with them finish as they started. Stored acceptance artifacts stay readable.

**Code review is one verification pass.**

- Run the full suite on the exact revision and check the merge with main.
- Read the diff against the agreed plan, and write a throwaway reproduction
  for the risky spots (concurrency, money, data boundaries); that is where
  every real post-implementation defect came from. Reproductions are not
  committed.
- Block only on defects and missed requirements; polish goes in
  non-blocking notes.
- Name anything that could not be run (for example Docker) as not verified,
  instead of approving it by inspection.
- After a correction, re-review only the fix (as ADR-007's incremental
  re-review already does).

**Clarification gathers repository facts.** The brief records the facts the
plan will depend on (schema constraints, framework behavior, existing tests
touched), which is what most plan-review findings were about. Aim: fewer plan
rounds.

## Settings redesign

Riding along because this plan adds settings. The app Settings
([AppSettings.tsx](desktop/src/views/AppSettings.tsx)) is one long modal: every
section stacked under small uppercase headings, cramped, with no separation
and weak keyboard and screen-reader support. It becomes a larger modal laid
out like macOS System Settings: a sidebar of tabs on the left, the selected
tab's settings on the right.

| Tab | Contents (existing sections, regrouped) |
| --- | --- |
| General | Appearance, Developer mode, When you quit |
| Notifications | Desktop notifications and their kinds |
| Marketplace | Account and sign-out |
| Backup | Create and restore backups |
| About | Version and updates, Docker, Compose, services, free disk |

- Each tab's settings sit in grouped rows (label left, control right, a short
  description under the group), not a stack of paragraphs.
- Accessibility: the sidebar is a `tablist` with arrow-key navigation, the
  dialog is labelled and traps focus, Escape closes it (except while a backup
  runs), focus is visible, and headings follow the tab structure.
- The last tab opened is remembered. On a narrow window the sidebar becomes a
  row of tabs above the content.
- Project settings and the assistant's settings stay where they are (project
  page and Agents page). The effort ceiling (G5) joins the assistant's
  settings, and model tiers (G2) live on the Agents page with the agents that
  offer them.

## Branch

`feat/model-guide`, from `main`.

## Slices

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| G1 | `feat(coordinator): ship an official model guide` | `internal/modelguide` with the embedded guide, tiers, and `as_of`. Overrides table (migration): model ID, tier, official tier at the time, updated at. Merged view: `GET /api/v1/model-guide` (each entry with tier, note, source official/user/unrated); `PUT` and `DELETE /api/v1/model-guide/overrides/{model}`. Premium and unrated come from the merged guide instead of `ask_before`; migrate `ask_before` as above. Tests for merging, unrated, migration, and premium checks. Update `coordinator-api.md`. | — |
| G2 | `feat(desktop): set model tiers on the Agents page` | Tier per model with reset; replaces "Ask before using". The changed-official-tier notice with Keep mine / Use official, on the Agents page. | G1 |
| G3 | `feat(worker): pass reasoning effort per turn` | `effort` in the worker turn contract. Codex: pass it on the turn. Claude: report supported efforts in the catalog and pass the chosen one. Record effort with each attempt's usage (migration). Adapter and persistence tests; update `worker-api.md`. | — |
| G4 | `feat(orchestration): choose models and effort per phase` | Work orders store an optional planning model and effort per role, and an effort for the rest (migration). Empty planning model means the role's model. The worker router picks each turn's model and effort by its phase (planning, its recovery, and plan confirmation use the planning choice), so recovery follows on its own. `create_work_order`, `start_work_order`, and the Start API accept the new fields; switching a role's model clears its planning model. The Start form adds each role's effort and "Different model for planning", collapsed by default. | G3 |
| G5 | `feat(coordinator): cap the assistant's effort` | Assistant setting for the highest effort, default `high`; the coordinator refuses an assistant choice above it, with a reason. Settings UI. | G3 |
| G6 | `feat(assistant): choose from the model guide` | The assistant's state lists the merged guide for offered models; its instructions replace the one-line model preference with the steps, phase, effort, and escalation rules above. The Start form's suggestion uses the guide (Strong lead, Strong reviewer on the other provider). Settings page recommends a Frontier model for the assistant. | G1, G4, G5 |
| G7 | `feat(desktop): show models in the cost breakdown` | The usage API already returns totals per model; split them within role and phase, add effort, and show both on the work-order cost line. | G3 |
| G8 | `feat(orchestration): split plan review from code review` | Runs record plan review and code review separately (migration; existing runs map `skip_reviewer` to both). Code review requires plan review. The orchestrator ends a Reviewed plan run after implementation at the merge gate, as Planned does. Presets gain Reviewed plan in the Start form, `create_work_order`, and the API. Recovery and the UI follow the recorded steps. Tests for each preset's stages and recovery. | — |
| G9 | `feat(orchestration): remove independent acceptance tests` | New runs never author them; the toggle leaves the Start form, the assistant's actions, and its instructions; the API accepts and ignores the field. Runs started with them finish as they started. Update `feature-artifacts.md` and `coordinator-api.md`. | — |
| G10 | `feat(orchestration): make code review one verification pass` | Code review prompt: suite and merge first, diff against the plan, throwaway reproductions for risky spots, block only on defects and missed requirements, name what could not be run. Clarification records repository facts. Update prompt assertions. | G9 |
| G11 | `feat(desktop): redesign Settings with sidebar tabs` | Sidebar-tab Settings modal as above, reusing the existing sections; dialog and tablist accessibility; remembered tab; narrow-window layout. Every tab's panel stays mounted, so a running backup keeps reporting. (The desktop's tests run without a DOM, so the dialog is checked by hand.) | — |
| G12 | `docs: record the model guide` | ADR-031 accepted; amend notes on ADR-018, ADR-022, ADR-027; mark this plan done. | G1–G11 |

G1, G3, G8, G9, and G11 are independent and can land in any order. G6 lists the
Reviewed plan preset once G8 has landed.

## Verification

Baseline from the local history: Full runs with a `gpt-6.1-sol` lead read
about 7.8M input tokens and 6 lead turns per run, Planned runs about 5.6M and
2.7 turns; an Opus 5.5 reviewer adds about 2M input and 25k output per run.

After G6 and G10, run comparable work (one docs change, one ordinary feature,
one data/logic change) and compare from G7's breakdown:

- The docs change runs Quick on a Light or Strong model.
- The feature runs Reviewed plan; its plan review still raises findings, and
  its tokens drop by roughly the post-implementation share (about a third).
- The data/logic change runs Full; plan review raises the same kind of
  findings, and code review is one pass that either approves or blocks on a
  real defect.

Then check what the lighter presets let through: count how often a Planned
or Reviewed plan run is followed by a fix work order on the same area.

## Risks

- **A stale guide.** Mitigated by exact IDs, unrated-needs-approval, and
  release updates. The as-of date shows in the Agents page.
- **Cheaper implementers fail more.** Mitigated by Light only for mechanical
  steps, escalation after two rounds, and code review.
- **Defects that code review would have caught.** Reviewed plan drops the
  check that found races and merge-time bugs. Mitigated by keeping Full for
  data, money, auth, migrations, and concurrency, and by the validation merge
  gate.
- **Phase switches and recovery.** A crash around plan confirmation must resume
  on the right model; recovery tests cover it.
- **Claude effort plumbing.** How Claude Code takes an effort per turn must be
  confirmed in G3; if it cannot, Claude turns keep their default and the
  catalog lists no efforts for them.

## Not in this plan

- **A budget posture** ("Save usage / Balanced / Best quality") shifting the
  rules by a tier. The effort ceiling and tiers cover most of it; revisit with
  data.
- **Looking the guide up automatically.** Public sources disagree and are
  often vendor-run; the guide is curated and shipped.
- **Learning tiers from local run data.** G7 makes the data visible first.
- **A rules editor, per-project overrides, cross-provider phase changes.**
