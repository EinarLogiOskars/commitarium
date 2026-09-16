# Graphical-world art (gitignored)

This directory holds the **artwork** for Commitarium's graphical world. It is
intentionally **not** committed to the public repository.

## Why it's empty in a clone

The world's code, logic, and scaffolding are open source and live in the repo
(see `src/views/PixelWorld.tsx` and related). The **art** is licensed from
third-party pixel-art packs whose terms do not permit free redistribution, so
the raw image files are gitignored.

At runtime the app **detects** whether this directory contains art:

- **Art present** → the graphical world is available (official app builds bake
  it in).
- **Art absent** (a fresh clone/fork) → the world feature is disabled and the
  app runs as the normal GUI. Nothing else is affected.

## Supplying your own art

You can enable the world yourself by placing compatible art in this folder. Two
options:

1. **Buy the packs** we use and drop their sprites in here (see
   `CREDITS.md`/the world memory for which packs), then wire the paths.
2. **Make your own** pixel art matching the expected sprite contract.

This keeps the whole system forkable and usable without the proprietary art —
build the world with your own assets, or run the app without it.
