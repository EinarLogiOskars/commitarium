# Project Previews: Implementation Plan

## Goal

Let the user run the project's application from inside Commitarium and look at
merged work before it reaches their own repository. The preview runs the
canonical head, opens in the browser, and is always one click away in the
project rail.

- **Part 1 (done, ADR-013):** a single container started from the stack's
  `run` commands, with all data temporary.
- **Part 2 (this plan, ADR-014):** previews run the repository's own compose
  file, validated and rewritten by Commitarium. That brings persistent data
  through named volumes, services like Postgres, and a compose file the user
  can also run locally. The `run` commands from Part 1 are replaced.

## Part 1: done

| Commit | What |
| --- | --- |
| `94b9c7a`, `d95d46f` | ADR-013, contract docs, plan |
| `d00168a`, `61ee2a6`, `5e4daf9` | `run` in the stack manifest, proposed by the assistant, shared with agents |
| `6be25f7` | Tauri preview runner, logs, exit stop, startup sweep |
| `905bcfc`, `d9a579f`, `10bda44`, `0feb160` | Bindings, Run section in the stack view, rail item, Preview page |

What stays from Part 1: the preview manager, the start/stop/status/logs
commands and event, fetching the canonical head, readiness checks, exit stop,
the startup sweep, the rail item and the Preview page.

## Part 2 decisions (ADR-014)

1. **The compose file is the source.** It sits at the repository root under a
   name `docker compose` finds by default. The stack's `run` shrinks to
   `{ "open": { "service": "web", "port": 5173 } }`; `setup` and `processes`
   are removed.
2. **Validate, then rewrite.** Run `docker compose config --format json` in the
   checkout, check the normalized model against an allowlist, and run a
   Commitarium-written file, never the original. The allowlist is in ADR-014.
   The key rules are that bind mounts, build contexts, and Dockerfiles must
   resolve inside the checkout, and volumes may not use `driver_opts` or
   `external`.
3. **Rewrite rules:**
   - The compose project name is `commitarium-preview-<project id>`.
   - Published ports become `127.0.0.1::<container port>`.
   - Each service gets the preview label, `no-new-privileges`, and CPU,
     memory, and PID limits.
4. **Data persists.** Stop, restart, and app exit use `docker compose down`
   without `-v`. Reset data removes the volumes. Project deletion removes the
   volumes and the images built for the preview.
5. **Authoring:**
   - Agents get the compose rules in their context.
   - The setup assistant proposes the `open` target.
   - A project without a compose file gets one through a normal work order,
     which the Preview page offers to start with a prefilled description.

## Contract

Authoritative text: `docs/coordinator-api.md` (Project toolchains) and
`docs/desktop-ipc.md` (Project previews). The changes from Part 1 are:

- `run` is now `{ open: { service, port } }`.
- `PreviewUrl.process` becomes `PreviewUrl.service`. There is one URL per
  published port.
- New command: `reset_preview_data(projectId)`.
- Log lines: `[preview]` for validation and builds, and compose's
  `<service> | ` prefix for service output.
- `start_preview` refuses to start when the head has no compose file.

## Slices

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| D1 | `docs: add ADR-014 for compose previews` | ADR-014, ADR-013 status, contract docs (`cbb9f1b`), and this plan. | — |

### Coordinator and Tauri backend

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| C6 | `feat(toolchain): replace run commands with an open target` | `Run` becomes `{ Open: { Service, Port } }`, with validation per the API doc. The assistant prompts propose the open target and assume a root compose file. Existing manifests with the old `run` shape load with `run` dropped, so the user sets the open target again. Tests. | D1 |
| C7 | `feat(orchestration): share compose rules with agents` | Replace the Part 1 run conventions in the lead and reviewer context. The preview runs the root compose file. List the allowed keys and the mount and build-context rules. Dev servers bind `0.0.0.0`. Data goes in named volumes. A frontend reaches its API through a relative-path proxy. The reviewer flags compose changes that break these rules. | C6 |
| C8 | `feat(desktop): validate and rewrite preview compose files` | New `preview_compose.rs`: run `docker compose config --format json` in the checkout, apply the ADR-014 allowlist and path checks, and produce the rewritten model per decision 3. The error names the offending service and key. Unit tests over JSON fixtures, covering one accepted full-stack example (web + api + postgres with a named volume and a bind mount inside the checkout) and a rejection test for each disallowed feature, including `driver_opts`, external volumes, `..` and symlink escapes in bind sources, and build contexts outside the checkout. | D1 |
| C9 | `feat(desktop): run previews with compose` | Replace the single-container runner. `start_preview` fetches the head, refuses to start without a root compose file, validates with C8, writes the rewritten file to the temp dir, and runs `docker compose -p <name> -f <file> up -d --build`, logging pulls and builds as `[preview]`. Resolve URLs with `docker compose port` and mark running when the open target answers HTTP. Mark failed when a service exits non-zero or the open service stops. Logs come from `docker compose logs --tail 400`. Stop, replace, and exit run `down` without `-v`. The sweep finds leftovers by the compose project label and keeps volumes. | C6, C8 |
| C10 | `feat(desktop): reset and delete preview data` | `reset_preview_data`: `down -v` for the project. Project deletion (`import::delete_project`) also runs `down -v --rmi local` for that project. | C9 |

### Security fixes from review (C11)

A review of C8–C10 confirmed these holes on a real Docker host. Each one lets
an agent-written compose file reach the host. Fix them before previews run
untrusted compose files. Add a rejection test for each.

| # | Finding | Fix |
| --- | --- | --- |
| 1 | **Host file and environment leak (critical).** `docker compose config` reads `env_file`, `include`, `extends.file` (and `label_file`) at absolute host paths and inlines their contents into `environment`. It also interpolates `${VAR}` from the desktop app's full environment, e.g. `${HOME}`. Confirmed: a host secret file and `HOME` came out in the validated model. | Run `config` (and `up`) with a cleared environment (`env_clear()` plus minimal `PATH`, `HOME`, `DOCKER_*`). Before running `config`, parse the raw YAML and reject `env_file`, `include`, `extends`, and `label_file` whose paths resolve outside the checkout. Reject `$` interpolation, or use `--no-interpolate`. |
| 2 | **Volume `name:` adopts any Docker volume (critical).** `volumes: {v: {name: commitarium_claude-profile}}` mounts that existing volume read-write, and Reset or Delete then removes it with `down -v`. | Reject `name` on top-level volumes, or force it to `<project>_<key>`. |
| 3 | **Network `name:` joins any network (critical).** `name: commitarium_default` puts the preview on the coordinator's and Forgejo's network. | Reject `name` on top-level networks, or force it. Also reject `ipam` and `attachable`. |
| 4 | **`image` with `build` overwrites trusted tags (high).** A preview can build agent code tagged `commitarium-coordinator:latest` or a worker image, and Commitarium runs it later with credentials. | When `build` is present, force `image` to `<project>-<service>`, or reject the combination. |
| 5 | Service-level `networks` values are not checked (`ipv4_address`, `mac_address`, `driver_opts`). | Allowlist them: `aliases` at most. |
| 6 | `x-*` keys are accepted at every level. | Accept them only at the service and top level, as ADR-014 says. |
| 7 | User `labels` are allowed on volumes and networks. | Drop them, or forbid the `com.docker.*` and `commitarium.*` prefixes. |
| 8 | Delete removes images by label. It is unconfirmed whether that catches built images. | Becomes moot once fix 4 forces the tag; verify `--rmi local` removes them. |

### Follow-up from re-review (C12)

The re-review of `cfb111b` confirmed that findings 2–8 are fixed. Finding 1 is
fixed for `env_file`, `include`, `extends`, `label_file`, merge keys,
multi-document files and repository `.env`, but it has a bypass:

| # | Finding | Fix |
| --- | --- | --- |
| A | **YAML escapes get around the `$` ban (critical).** The raw-byte `$` check (`preview_compose.rs:584`) misses the double-quoted escape `"\x24{...}"`. `config --no-interpolate` keeps it as `${...}` in `preview.compose.json`, and `up` interpolates it. Confirmed: a bind source through directories literally named `${Z:-..}` passed the path check and mounted a host file outside the checkout at `up`. Any host path can be reached this way, including the Docker socket, and so can build contexts. Values such as `"\x24{DOCKER_CONFIG}"` also resolve to host values. | Validate the **normalized** model, not raw bytes. Either reject any `$` in any key or string of the model, or escape every `$` to `$$` before writing `preview.compose.json`. Add tests with `\x24` and with literal `$` in paths. |
| B | **Volume and network names can collide across previews (low).** The forced `<project>_<key>` lets volume key `a_data` in project `zzh` collide with project `zzh_a`'s `data` volume. It isn't reachable with today's `prj_<hex>` IDs. | Reject `_` in volume and network keys, or use a separator that can't appear in project IDs. |

### Last follow-up (C13)

The re-check of `c5952c3` confirmed A and B are fixed. One low issue remains:

| # | Finding | Fix |
| --- | --- | --- |
| C | **Key-only `environment` entries pass through the up-time env (low).** `environment: [PATH, DOCKER_CONFIG, HOME]` or `{DOCKER_CONFIG: null}` passes validation, and at `up` the container sees the host `PATH` and `DOCKER_CONFIG` path. The cleared environment keeps secrets out, so only paths and the username leak. | Reject null values in the normalized `environment` map, and list entries without `=`. |

### Renderer

| # | Commit | Contents | Depends on |
| --- | --- | --- | --- |
| R5 | `feat(desktop): open target in the stack view` | Types: `run.open`, `PreviewUrl.service`, `resetPreviewData`. The Run section in `StackPicker` becomes a service field and a port field, with a short note that the preview runs the repository's compose file. Assistant proposals show the open target. Remove the Part 1 setup and process editor. | D1, C6 |
| R6 | `feat(desktop): compose states on the preview page` | The Preview page checks the repository overview for a root compose file. If there isn't one, it explains and offers **Set up preview**, which opens New work order prefilled with a title and a description asking for a compose file under the preview rules. A **Reset data** button with a confirm. URL buttons labelled by service. Log rendering handles the compose prefixes. | R5, C9 (C10 for reset) |

### Order

```
D1 ─┬─ C6 ─┬─ C7
    │      └─────┐
    ├─ C8 ───────┴─ C9 ─ C10 ─ C11 ─ C12 ─ C13
    └─ R5 (needs C6) ─ R6 (needs C9)
```

## Later (not in this plan)

- **Live previews for work sessions.** Bind-mount the session workspace into
  the compose services (allowed by the inside-the-checkout rule) and let dev
  servers reload. File watching on Docker Desktop for macOS may need polling.
- Previewing a work order's reviewed commit before merge.
- Widening the compose allowlist as real projects need more keys.
