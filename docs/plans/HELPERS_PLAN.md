# Mayka's Helpers and Output Files: Implementation Plan (0.14.0)

**Status:** scope agreed 2026-11-07 (release order after 0.12.1); plan written
2026-11-08; H1 and H2 built; test accounts (H3) moved to the next release. Builds on ADR-035 (the assistant starts helpers) and
the research helper.

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
- **Formats.** The format the user asked for wins (docx, pptx, xlsx, PDF,
  JPEG, …): Mayka puts it in the brief, and the helper installs a library
  into its own directory when it needs one. Without one, the defaults: a deck
  is a 16:9 PDF, one slide per page, rendered with the worker's headless
  Chromium from an HTML source it keeps too; a report or document is
  Markdown; data is CSV; an image is PNG; a logo is an SVG plus PNGs at 512,
  1024 and favicon sizes. What it truly cannot make, it makes the nearest of
  and says so. Commitarium does not convert.
- **Outputs card.** A rail card, shown only when the scope has output files,
  newest first, each with who made it and when. It also shows in loose chats
  (no project yet). Opening a file shows the viewer.
- **Viewer and PDF export.** Markdown renders as a page; HTML renders in a
  sandboxed frame (no scripts reaching the app); images (SVG only as an
  image), text and CSV show as they are; a PDF shows its pages and can be
  presented full screen, a slide at a time. Files it cannot show (docx,
  pptx, xlsx, zip, …) show their details with **Open** in the user's default
  app. **Export PDF** (Markdown and HTML) renders the page in a print window
  of its own (a custom scheme, scripts off, no commands granted) and opens
  the system print dialog there, where macOS and Windows both offer Save as
  PDF. **Save** writes the file as it is; **Delete** removes it, after a
  native confirm.
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

### H3: project test accounts (moved to the next release)

Checked 2026-11-08: a helper cannot reach the preview yet. The preview
publishes its port on the computer's 127.0.0.1 in a Docker network of its
own, and a helper runs in a worker container, where 127.0.0.1 is the
container itself. The worker image also has Chromium's libraries but not
Chromium. Test accounts without a way to use them would be hollow, so the
user decided to ship 0.14.0 without them. They come in the next release,
together with real testing:
- the host connects a helper's worker to the preview's network (or gives
  it a reachable address) for the length of the helper;
- Chromium goes into the worker image, or is installed on demand;
- the project's test accounts (in Settings, never in the chat) are given to
  the helper as a file, and it reports what it found with screenshots in
  its outputs.

### H4: release 0.14.0

The version bump, then an rc on the test VPS, then the release.

## Not now

- Helpers that ask Mayka questions mid-task (research doesn't either).
- Converting between formats, or editing outputs in the app.
- Sharing outputs with other people, or publishing them.
