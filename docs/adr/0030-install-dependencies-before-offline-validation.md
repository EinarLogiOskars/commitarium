# ADR-030: Install dependencies before offline validation

- Status: Accepted (amends [ADR-010](0010-use-approved-project-environments-and-host-launched-validation.md))
- Date: 2026-10-16

## Context

Commitarium runs validation commands and the reviewer's acceptance tests
itself, in a disposable container that copies the reviewed commit and runs
with no network (ADR-010). A Python project's every run failed there with
`No module named pytest`: agents install dependencies outside the repository
(pip's site packages, Go's and Cargo's caches), so a copy of the repository
has none, and an offline container cannot fetch them. JavaScript projects
passed only because `node_modules` sits inside the repository folder. Each
test then showed "Commitarium: failed", though none had run.

The commands also ran in a login shell, whose `/etc/profile` reset `PATH`:
they used the image's own Python and Node, not the project's pinned
toolchain.

ADR-010 named the remedy: a restricted network preparation stage when package
managers cannot produce offline dependency state.

## Decision

A validation run installs the project's dependencies online, then goes
offline before any command runs, in the same container:

- **Install.** The container starts on Docker's default bridge network,
  copies the commit (and merges the reviewer's tests), and installs what the
  repository's standard files declare: `requirements*.txt` and
  `pyproject.toml`/`setup.py` into a `.commitarium-venv` in the copy;
  `pnpm-lock.yaml`, `yarn.lock`, or `package-lock.json` (only when
  `node_modules` is missing); `go.mod` with its caches in the copy. Rust is
  not covered yet.
- **Offline.** The desktop then disconnects the container from the network
  and tells it so; the container checks that no route out is left and
  refuses to run the commands otherwise. The commands run as before:
  read-only root, no capabilities, bounded, now in a plain shell so the
  project's toolchain and the installed dependencies are on `PATH`.
- **Honest results.** A run that could not run the tests — the install
  failed, or every command failed on a module, package, or command from
  outside the repository — is reported as such: the acceptance tests record
  why (`coordinator_error`) and no test gets a verdict. A module of the
  project itself missing remains a real failure.

## Consequences

- Python and Go projects get Commitarium's independent check; JavaScript
  projects install what is missing; all run on the project's own toolchain.
- The install step runs the project's install scripts with network access, as
  the agents' own containers already do; the validation container holds no
  credentials.
- Projects whose tests need more than these files declare (system libraries,
  extras under other names) still show why the run could not run them.
