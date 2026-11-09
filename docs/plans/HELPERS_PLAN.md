# Mayka's Helpers and Output Files: Implementation Plan (0.14.0)

**Status:** scope agreed 2026-11-07 (release order after 0.12.1); plan written
2026-11-08; H1 and H2 built. On 2026-11-09 the user brought test accounts,
the preview bar and testing helpers back into 0.14.0 (H3, H5, H6), built
overnight and tested live together before the release; H6 built in full,
not yet tested live. H7, a separate local install of the real app, added
and built the same day, not yet tested live. Builds on ADR-035
(the assistant starts helpers) and the research helper.

Mayka can brief a helper for any task, not just the four fixed ones (vision,
brief, stack, research). A helper researches and then writes a report or a
pitch deck, drafts a document, or tests the project's app with its test
accounts. What it makes comes back as **files**: shown in the chat where
Mayka hears of them, and gathered on an **Outputs** card with a viewer and
PDF export.

## What was decided

- **One general helper, briefed by Mayka.** A new action, `delegate`: a
  short `title` and a `description` that is the full brief (what to make,
  for whom, in what form, what it should find out first). Like research it
  starts at once, without a card, on the assistant's agent and model, and it
  may search the web. Research stays as it is: a quick question of fact.
- **It runs like research, but may write.** One consultant attempt in its own
  empty directory, with write access there and nowhere else. It also gets
  the scope's documents (the files the user attached). It gets no copy of
  the repository: Mayka reads the code herself and puts what matters into
  the brief.
- **Files come back.** The helper puts what it makes in `outputs/` in its
  directory. When it finishes, Commitarium copies those files into the
  scope's output store, then deletes the directory. Its summary and the file
  names go to Mayka as an event, and the event line in the chat shows the
  files as chips you can open.
- **Formats.** Markdown for documents and reports, one self-contained HTML
  file for decks and anything visual (slides as sections, print styles for
  one slide per page), and images, CSV or PDF when that is the natural form.
  The helper is told this; Commitarium does not convert.
- **Outputs card.** A rail card, shown only when the scope has output files,
  newest first, each with who made it and when. It also shows in loose chats
  (no project yet). Opening a file shows the viewer.
- **Viewer and PDF export.** Markdown renders as a page; HTML renders in a
  sandboxed frame (no scripts reaching the app); images, text and CSV show
  as they are; PDF opens in the frame. **Export PDF** renders the page in a
  print window of its own (a custom scheme, scripts off, no commands
  granted) and opens the system print dialog there, where macOS and Windows
  both offer Save as PDF. **Save** writes the file as it is; **Delete**
  removes it, after a native confirm.
- **Helpers show in the Agents card.** A delegated helper appears there while
  it works, like the brief writer does. Loose chats get the Agents card while
  a helper works there.
- **Project test accounts** move to the next release, with helpers that
  test the running app (see H3). Each project will get a list of test
  logins for its own app in Settings; passwords never pass through the
  chat, and a helper gets them as a file.
- **Limits, kept simple.** 20 MB a file, 200 files a scope; the oldest go
  first, as with loose-chat attachments. Deleting a file is in its viewer.
  Deleting a project removes its outputs with it.

## Steps

Each step is a PR on its own, reviewed before it merges.

### H1: the general helper and the output store (coordinator)

- `internal/outputs`: a store like `internal/documents`, at
  `<toolchainRoot>/outputs/<owner>/`, with an index of each file's helper,
  title and time. List, Path, Delete, DeleteProject, and Collect (copy a
  directory's `outputs/` in, renaming on collisions and skipping links,
  empty and oversized files).
- `delegate` action in `featureartifact` (validated like research), the
  prompt text in `turn.go`, and `HelperTask` in `conversations`:
  `StartTask` and `readTask` beside research, with a ReadWrite attempt in
  `WorkspaceRoot/tsk_<hash>` and the scope's documents delivered there.
- When it finishes, the files are collected, and the event and the pending
  event for Mayka name them. `Message` gains `Outputs []string`, so the event
  line carries them.
- Routes: `GET /api/v1/outputs/{owner}` and `GET`/`DELETE
  /api/v1/outputs/{owner}/{name}` (HTML served under a sandbox policy);
  agents-at-work lists general helpers (role `helper`), and
  `GET /api/v1/agents-at-work` serves loose chats.
- Tests: an action that validates, a run that collects files, the event that
  names them, the store's limits.

### H2: files in the chat, the Outputs card and the viewer (desktop)

- Output chips on event lines, opening the viewer.
- `OutputsCard` in the rail (projects and loose chats), shown only when
  there are files.
- `OutputViewer`: Markdown, sandboxed HTML, images, text/CSV and PDF;
  Export PDF (print) and Save.
- Agents card: the helper role's label, and the card in loose chats while a
  helper works.

### H3: project test accounts

Agreed 2026-11-09. The preview is a test copy with test data; the accounts
are test logins for the user's own app, never real ones.

- **Commitarium makes them, the app seeds them.** Each project keeps a list
  of test logins (label, username, password, role, note), stored by the
  coordinator per project next to its secrets, never in the repository or
  the chat. Commitarium generates the username (`<label>@commitarium.test`)
  and a strong password; the user can add, change a role, copy a password,
  or remove one in the project's **Settings → Test accounts**, which says
  they work only in the preview.
- **Seeded idempotently on every preview start.** The preview gets the list
  as a read-only file (`COMMITARIUM_TEST_ACCOUNTS=/run/commitarium/test-accounts.json`,
  alongside `COMMITARIUM_PREVIEW=1`). A work order Mayka proposes once has the
  app read it at start, in the preview only: create missing accounts, set
  their password and role, and remove `@commitarium.test` accounts no longer
  listed; nothing else in its data changes. The preview's data volume is
  kept, so data the user or a helper made survives; after Reset data or in
  a fresh session preview the accounts come back by themselves.
- **Mayka** sees the labels, roles and notes, never passwords, and knows
  whether the app seeds them yet.
- **Built as:** `internal/testaccounts`, one 0600 file per project under
  the coordinator's credentials directory (left out of backups, like the
  linked repositories' tokens); routes under
  `/api/v1/projects/{id}/test-accounts` (list without passwords, add,
  change role or note, remove, copy one password, and `preview`, the file
  itself). The host reads `preview` when it starts a preview (never a
  deployment or a preview check) and writes it into the preview's own
  directory, removed when the preview stops; a preview the coordinator
  cannot answer for starts without the file, so the app leaves its logins
  alone.

### H5: the preview bar and two ways to open the real app

- Every preview window shows a bar Commitarium draws (the app cannot hide
  it): "Preview: a test copy with test data", with **Open in Commitarium**
  (the real app's window) and **Open in browser** (its local address, or its
  domain or Tailscale address on a server). On this computer the real app is
  a stack of its own (H7). The button the user used last
  for that app comes first. The buttons show only once the app is installed
  or deployed; before that the bar says so.
- The Apps page cards get the same two buttons, remembered the same way.
- Mayka calls a preview a test copy and points to the real app the same way.

### H6: helpers that test the app

Checked 2026-11-08: a helper cannot reach the preview today. The preview
publishes its port on the computer's 127.0.0.1 in a Docker network of its
own; a helper runs in a worker container, where 127.0.0.1 is the container,
and the worker image has Chromium's libraries but not Chromium.

- Mayka's `delegate` may ask to test the app. The helper then gets a
  throwaway session preview of the project's main branch (seeded, so the
  test accounts work), a way to reach it for the length of the helper and
  nothing else, a browser, and the accounts as `test-accounts.json` in its
  directory. It reports with screenshots in its outputs; the preview is
  removed when it finishes.
- If reaching the preview safely turns out bigger than this, H6 stops at a
  clear point and the rest is written down, not forced.

**Built as** (2026-11-09, overnight, not yet tested live):

- **Mayka asks** with `test_app` on `delegate` (validated: delegate only;
  StartTask refuses it outside a project). The prompt says when: trying the
  app as a user would, once its preview works, on what is merged.
- **A test preview is a record the host acts on**, like a preview check,
  because only the host runs Docker. `internal/testpreviews` keeps them in
  one 0600 file beside the coordinator's database (no worker mounts it):
  pending → starting (host claimed) → ready (address) or failed (reason) →
  ending → gone once the host says it removed it. At most 2 at once; any
  older than 3 hours ends by itself; deleting the project ends its own.
  Routes for the host: `GET /api/v1/test-previews`, `POST
  /api/v1/test-previews/{id}/claim|ready|failed|removed`.
- **The helper waits, then starts.** `StartTask` with test_app only asks for
  the test preview and keeps the brief and model on the helper. Following
  it, the coordinator starts its one attempt once the preview is ready,
  writing `test-accounts.json` into its directory and the address, the
  login file and how to drive the browser into its brief. A preview that
  fails ends the helper before it starts, with the host's reason, for
  Mayka. When the attempt ends, however it ends, the test preview ends and
  the directory (logins included) goes.
- **The host** (`test_preview.rs`, polled with the other jobs every 10 s)
  claims a pending one; fetches the stack, the handoff and the test
  accounts; starts the default branch's head in a compose project of its
  own (`commitarium-preview-<project>-tsk_<id>`, validated and rewritten
  like any preview, test accounts mounted); waits as a preview check does;
  connects the helper's agent worker (`agent-<id>-worker` of the stack) to
  the open service's own network; checks with `curl` from the worker that
  it answers; and reports `http://<the service's IP there>:<port>` (the
  coordinator accepts only http, a private IPv4 address and a port). An IP
  rather than a name, so no alias collides with the stack's names, and
  dev servers that check the Host header accept it.
- **Reach mechanism and what it opens.** `docker network connect` of the
  worker to that network, undone with `docker network disconnect --force`
  of every container on it that is not the test preview's own, before its
  containers, networks, volumes, images and sources are removed. Works the
  same on Docker Desktop and Linux (checked on Docker Desktop 29.8: no
  reach before connect, HTTP 200 after, the network cannot be removed
  until the worker is disconnected, and the teardown does that; an
  ignored live test covers it). While connected:
  - the worker reaches every container of the throwaway preview (the app
    and its database), on any port. They hold test data only;
  - the worker is shared by every turn on that agent, so other work orders
    and helpers on it could reach the preview too. They already see every
    project's checkouts under `/workspaces`, so no project boundary moves;
  - the preview's containers (agent-written project code) can reach the
    worker's listening ports on that network: the worker API, which wants
    its bearer token, and anything an agent serves on 0.0.0.0. They still
    cannot reach the coordinator, Forgejo or other workers: Docker does not
    route between networks and the worker cannot forward (no capabilities).
  Nothing else changes: workers still get no Docker socket, no
  capabilities, and the same networks otherwise; the host decides the
  network and the container from validated IDs and Compose labels, never
  from what the coordinator or a worker says. As before, any worker can
  call the coordinator's API; the worst it can do here is mark a test
  preview failed or give a helper a wrong private address.
- **Cleanup survives restarts.** A host that starts removes every test
  preview's compose project (worker disconnected first), and reports any
  record still starting or ready that it did not start as failed. Every 5
  minutes it removes test previews whose records are gone or failed. A
  coordinator that restarts keeps its records and its helpers, and a helper
  it can no longer follow ends its preview after 3 hours. Session prunes
  and the project's preview data leave test previews to this path.
- **The browser** is baked into both agent worker images: Playwright 1.64.1
  and its headless Chromium shell under `/opt/commitarium-browser`, outside
  Playwright's default paths so projects keep their own versions. About
  315 MB more per image uncompressed (the images are about 1.5 GB), checked
  by building the same layer on `node:22-bookworm-slim`: it renders and
  screenshots as uid 65532 with all capabilities dropped and
  no-new-privileges (Playwright runs Chromium without its own sandbox by
  default; the container is the boundary). Installing on demand would save
  that, but each helper would then wait on a download that can fail.
  The helper runs its script with `PLAYWRIGHT_BROWSERS_PATH` and
  `NODE_PATH` set, and saves numbered screenshots and `test-report.md` in
  `outputs/`.
- **The test logins** reach the helper as a file in its directory under
  `/workspaces`, which every worker mounts, for the length of the helper.
  They are test-only logins for the preview, which gets the same file.

**Live testing owed:** a full run on this computer (Docker Desktop) and on
the test VPS (Linux): Mayka delegates with test_app, the test preview
starts and the helper's worker reaches it, the helper signs in with a test
account and leaves screenshots, and afterwards no `tsk_` compose project,
network or worker connection is left; also quit the app mid-test and
restart the coordinator mid-test. The worker images need rebuilding for the
browser. The UI shows nothing new: the helper sits in the Agents card while
it waits for its preview, as working.

**Not now:** testing a work order's session before it merges (its
checkout is a session preview; the helper would get that instead), showing
the test preview's progress or logs in the UI, and a narrower reach (a
proxy that forwards only the open port) if the shared worker's reach ever
matters more.

### H7: a separate local install of the real app

Agreed 2026-11-09, after review found that on this computer the preview and
the real app were one stack: the Apps page window and the preview window
framed the same containers and data volume, and the test accounts reached
the user's real data. The user chose a separate local install.

- **The real app is the project's deployment, here too.** The host already
  runs a server's deployment as its own stack (`<project>@live`, Compose
  project `<project>-live`, data kept from one deploy to the next, no test
  accounts). On this computer the real app is the same: its own containers
  and volumes, never given the test accounts file. The default branch's
  preview stays the test copy, with test accounts.
- **Version: it follows the main branch.** Before this the app on this
  computer followed main by itself, and that stays: opening the real app's
  window, or Open in browser, brings it to the default branch's head
  (`/v1/deployments/follow`: left as it is when already up at the head,
  otherwise deployed at the head, keeping its data), and the window moves
  it again when work merges. A server keeps pinned versions and explicit
  deploys: there other people use the app, and the user decides when they
  get a change. On one's own computer that ceremony adds nothing.
- **Installed** means on this computer what it meant in H5: the project is
  on the Apps page. Its real app starts the first time it is opened, so the
  bar's buttons show for every app; a project that is not an app has none.
- **Start and stop.** The real app runs when its window opens or Open in
  browser starts it, and the preview when it is previewed; neither starts
  the other. Closing the window leaves it running, as before; quitting
  Commitarium stops both (the host stops deployments with previews), and
  nothing starts the real app again until it is opened.
- **Data.** Reset data on the Preview removes the preview's Compose project
  only. Deleting a project stops its deployment, forgets its record and
  removes both stacks' data.
- **Where it is used.** The Apps page window and its tiles' buttons, the
  Marketplace's installs and Mayka's new-app card open the real app's
  window; the bar's Open in Commitarium opens it, and Open in browser its
  127.0.0.1 address. On a server nothing changes.
- **Built as:** the host's `Deployments::follow_head` and `forget`, the
  routes `/v1/deployments/follow` and the deletion through
  `/v1/previews/delete-project`; Tauri commands `start_real_app` (refused on
  a server) and `real_app_status`, granted to the main window and app
  pages; `real_app` reads the deployment's address. Mayka's prompt and the
  preview rules say the real app is a separate install.
- **Existing installs:** an app used before this keeps its data in the old
  preview's volume, which is now the test copy's; the real app starts empty.
  There are no users yet, so there is no migration; noted in the PR.

**Live testing owed:** open an app from the Apps page (a `-live` Compose
project starts, with no test accounts mounted), merge a work order and see
it move to the new head with its data, open the preview alongside and check
the two have different data, Reset data on the preview and see the real app
keep its own, Open in browser on a stopped app, quit Commitarium and see
both stop, and delete the project and see both stacks' volumes go.

### H8: work on a file together

Agreed 2026-11-09 after testing rc.1: a helper today is one-shot. It hands
back a finished file and stops; feedback ("make the titles blue") makes Mayka
brief a new helper with no memory of the first. That is the opposite of how
people work with an assistant on a CV, a deck or a logo. H8 makes an output
something the user and Mayka keep working on until it is right.

- **The file opens beside the chat.** Opening an output (a chip, the Outputs
  card, or automatically when a helper Mayka briefed for this chat makes its
  first file) shows it in a side panel next to the chat, not an overlay. The
  panel stays while the user talks and reloads whenever the file changes. The
  viewer's actions (Present, Export PDF, Save, Open, Delete) live there.
- **The helper stays with its file.** A helper's session and directory are kept
  after it finishes instead of being removed. Feedback goes back to **the same
  helper**, resumed with its earlier work, sources and files: a new action,
  `revise`, that Mayka sends at once (like `delegate`) with the user's change in
  her words. Small changes and big rework go the same way; the helper decides
  how much to redo. Mayka stays in the middle (ADR-035): she keeps no write
  access to files herself, and the user never talks to the helper directly.
- **It shows as it is built.** While a helper works, the panel shows its
  output folder's current files (served read-only from its directory), so the
  user can say "wrong direction" early. Mayka hears the user's message as
  usual; a revise sent mid-run waits for the turn to end and follows it.
- **Every change is a version.** Each time the helper finishes, its files are
  collected as a new version of the same output (`deck.pdf` v1, v2, …), not a
  new file. The panel steps back through versions and can restore one; the
  Outputs card shows the latest with its version count. Older versions count
  toward the store's limits.
- **When it ends.** The helper's directory and session are kept while the
  output is being worked on, and removed when the user marks it **Done**,
  deletes the chat or the output, or after 14 days without a revision (the
  versions stay). Revising after that starts a new helper that is given the
  latest version and the earlier briefs, so it continues instead of starting
  over.
- **Mayka knows what is open.** Her state lists the chat's outputs with their
  helper, version and whether it can still be revised, so "make it shorter"
  goes to the file the user is looking at.

To check before building: the worker resumes a finished consultant attempt's
provider session for a new turn (Mayka's chats already do this), and keeping
helper directories does not collide with the test-preview cleanup (H6).

### H4: release 0.14.0

The version bump, then an rc on the test VPS, then the release.

## Not now

- Helpers that ask Mayka questions mid-task (research doesn't either).
- Converting between formats, or editing outputs in the app.
- Sharing outputs with other people, or publishing them.
