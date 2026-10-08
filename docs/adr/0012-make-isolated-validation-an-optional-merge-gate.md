# ADR-012: Make isolated validation an optional merge gate

- Status: Accepted
- Date: 2026-09-24

## Context

[ADR-010](0010-use-approved-project-environments-and-host-launched-validation.md)
introduced isolated validation as the exact-revision merge gate: an ordered list
of project commands run in a disposable, credential-free worker that had to pass
before the coordinator would merge. As first implemented the gate was
mandatory whenever the validation service was wired in. A project with no
validation configuration could not merge at all — it parked at the merge gate
asking the user to configure commands.

That surprised users. Projects created before the gate existed had merged on the
two agents' review alone; once the gate went live those same projects became
unmergeable with no configuration change on the user's part. It also conflated
two separate questions: *is there a check to run* and *must a check exist*.

A related question is whether repository CI — a Forgejo Actions runner plus a
workflow in the repository — makes coordinator-owned validation redundant. It
does not, for two reasons rooted in this project's model:

- Validation commands live in project settings, outside the repository. The
  agents cannot edit them. A CI workflow lives in the repository, where the
  agents author and can change it (see
  [ADR-009](0009-let-agents-own-internal-forge-actions.md)); a gate the code
  author can rewrite is a weaker safety check.
- The validation worker is disposable and credential-free by design. A standing
  Actions runner executing agent-authored workflow YAML is a larger, privileged
  attack surface.

Additionally, the default-branch protection reconciled by the coordinator uses a
merge whitelist and required approvals, not required status checks, and the
coordinator identity is on the merge whitelist. CI status therefore does not
gate a coordinator merge today without further integration.

## Decision

Isolated validation is optional. Its absence is a first-class state, not an
error.

- When a project has **no** validation configuration, there is no validation
  gate. Merge proceeds on the two agents' approval, exactly as before the gate
  existed. This holds at every enforcement point: pre-merge readiness, idle
  ready-to-merge recovery, and the merge action itself.
- When a project **has** validation configuration, the gate is unchanged: the
  exact approved commit must have a passing job for the current command list
  before merge.

The choice is surfaced, not hidden. The project's validation settings frame it
plainly — check the agents' work yourself, or trust them to keep themselves in
check — and note that the commands live outside the repository so the agents
cannot change them. Creating an auto-merge work order with no validation
configured, and reaching the human merge gate with none configured, both show a
warning with a direct link to the validation settings. Auto-merge without a
human at the gate stays silent, matching prior behavior.

Repository CI is treated as complementary, not a substitute for this gate: good
for fast feedback during implementation and pull-request signal. Making CI an
enforced merge gate would require required status checks in branch protection,
preventing the coordinator identity from bypassing them, and protecting the
workflow files from agent modification. That is out of scope here and would not
remove the reason to keep an agent-untamperable validation gate available.

This amends the mandatory-gate aspect of ADR-010. The rest of ADR-010 — the
disposable credential-free worker, coordinator-owned specifications and results,
and Tauri-owned execution — is unchanged.

## Consequences

- Projects without validation configuration merge on agent approval alone, and
  the UI says so at the points where it matters.
- Configuring validation is an explicit, reversible decision. Deleting the
  project configuration turns the gate off while preserving historical jobs.
- The `merge_not_ready` contract now requires passing isolated validation only
  when validation is configured.
- CI can be added later for feedback without conflicting with this gate; using
  CI as the enforced gate remains a separate, larger decision.

## Alternatives considered

### Keep validation mandatory

Rejected. It retroactively wedged existing projects and forced ceremony on
projects whose owner is content to trust agent review.

### Replace validation with repository CI

Rejected as a replacement. CI is authored by the agents and runs on a
privileged standing runner, and does not gate the coordinator merge without
additional branch-protection work. It is kept as a complementary feedback
mechanism instead.

## Reconsider when

- repository CI is wired as an enforced, tamper-resistant merge gate;
- branch protection begins requiring status checks the coordinator honors; or
- validation needs to become mandatory for a class of projects by policy.
