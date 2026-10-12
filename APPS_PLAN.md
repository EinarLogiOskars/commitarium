# Apps: Implementation Plan

**Status:** draft for review. Builds on the assistant (ASSISTANT_PLAN.md,
ADR-022) and autopilot (ADR-023).

## Goal

The user asks for an app ("a calorie tracker"), uses it in its own window, and
changes it by asking ("make the buttons blue"). The assistant drives the work;
the user sees results, not work orders, unless they want to.

The calorie tracker showed the parts already work: a vision written from one
sentence, and autopilot building it item by item to merged. Apps assembles
them into one flow and gives the result a place to live.

## Decisions

### What an app is

1. **An app is a project with an app flag.** Same repository, vision, work
   orders, preview, and assistant conversation. A project can become an app
   and back; every app still opens as a project ("Show the work").
2. **Apps live in their own area.** The project list stays for development;
   an Apps area shows apps as tiles with their state (building, ready,
   updating, needs you).

### Running an app

3. **An app runs in its own window, not the browser.** It runs on the existing
   preview runtime: Docker Compose from a fresh copy of the default branch,
   with its data in volumes that survive restarts. A Tauri window opens a
   Commitarium page that frames the app full-size and floats a "Change this"
   button over it.
4. **The app never touches Commitarium.** The app's page is in a frame from
   its own local address; the button and chat belong to Commitarium's page
   around it. The app gets no Tauri commands and no coordinator access.
5. **A merged change reaches the running app by itself.** When a work order
   for an app merges, the coordinator's event wakes the desktop to rebuild the
   app's preview from the new head and reload the window. Data volumes are
   kept.

### Building and changing

6. **New app is one sentence.** "New app" asks what to build, creates the
   project with the defaults, has the assistant write the vision, sets up the
   stack it recommends, and starts autopilot on the whole vision. The window
   shows progress ("Building: 3 of 8") until the first item that runs merges,
   then the app itself.
7. **Apps mode: the assistant drives.** In an app's conversation, autopilot is
   always on for the app's goal, and a change request becomes a goal of its
   own: the assistant adds it to the vision, and autopilot builds it. The same
   safeguards hold as in ADR-023: premium models wait for approval, reviews
   and merge gates run, and work that needs the user reaches them.
8. **"Change this" is the assistant's conversation for the app.** The button
   opens the drawer over the app window, scoped to the app. The user can
   point at what they mean later (a screenshot of the window attached to the
   message); the first version is text.

### What the user sees

9. **Results, not work orders.** An app's chat says "Making the buttons blue…"
   and "Done, reloaded", with "Show the work" linking to the work orders. The
   work stays fully visible in the project.
10. **Needs-you moments are the only interruptions.** A premium model, a
    question about the product, or a blocker the assistant can't fix shows
    in the app window and the inbox; everything else runs quietly.

## Phases

### Phase 1: Apps area and running apps

- **Data:** an `app` flag on projects (migration), set from project settings
  or when created as an app.
- **Desktop:** the Apps area (tiles, state from the project's runs and
  preview); "Open" starts the preview if needed and opens the app window
  (WebviewWindow with the framing page); the window reloads when the
  preview restarts.
- **Updates:** a merged work order for an app rebuilds its preview and
  reloads its window.

### Phase 2: New app and apps mode

- **New app:** one field, then project, vision, stack, and autopilot on the
  whole vision, chained by the coordinator so it survives the desktop closing.
- **Apps mode:** the app's goal is always on; a change request becomes a
  vision item and a goal; the assistant's instructions say it drives and
  reports results.
- **App window:** the "Change this" button and drawer; progress while
  building; needs-you prompts.

## Open questions

- **Several apps at once:** each preview takes ports and memory. Start apps
  on demand and stop idle ones after a while?
- **App icons and names:** generate an icon, or use initials on a color?
- **Sharing data between apps** (a meal planner reading the calorie
  tracker): later, if at all.
- **The UI library** (ASSISTANT_PLAN.md): apps start looking like one family
  once the user's preferences settle; not in this plan.

## Not in this plan

- Running apps outside Commitarium (exporting, installing on a phone).
- System-level capabilities for apps (the opt-in operational layer).
- App templates or a gallery.
