# Setup Simplification: Implementation Plan

**Status:** merged (PR #10) and in daily use.

## Goal

A new or imported project should reach a running preview without the user
choosing runtimes, writing a compose file, or naming ports. The stack and the
preview set themselves up; the user is asked only when Commitarium cannot.

## Decisions

1. **The stack is detected when first needed.** Reading a project's stack
   (the Stack page, the import flow, or creating a work order) detects it when
   none is saved, and saves it when detection is confident. An unsure
   detection leaves the manual editor and "Ask an agent", as before. A saved
   stack shows as one line with Change.
2. **The preview is set up in the background.**
   - When a project has a stack but its repository has no compose file,
     Commitarium starts a background quick change, "Set up the preview". It
     has no clarification, plan, or reviewer, and merges by itself once
     validation passes.
   - It is not listed with the user's work. The Preview page shows it running
     and links to it.
   - The agent follows the preview rules: dev servers with live reload and the
     source folders they watch mounted, and the open service and port named in
     `x-commitarium-preview` at the top of the compose file.
3. **The preview proves it.**
   - When the setup merges, the desktop starts the preview.
   - If that start fails, the desktop reports the error and Commitarium starts
     a background "Fix the preview" with it, up to two after a setup.
   - After that, the Preview page offers Fix with an agent, which opens a
     session with the error and the rules.
4. **The open target is inferred.** Without one on the stack, the preview
   opens the service and port in `x-commitarium-preview`, or the compose
   file's only published port, and saves it on the stack. The form appears
   only when neither tells.

## Not in this plan

- A per-project setting to turn the background setup off. It is on for every
  project for now. The setup work order and its pull request stay in history.
- Setting up databases or other external services.
