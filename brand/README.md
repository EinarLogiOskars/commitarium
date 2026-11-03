# Commitarium brand

The mark is a hexagon C holding a blue hexagon: the lead and the reviewer around
the commit they agree on. Both shapes sit on a triangle grid, and the blue
hexagon keeps the same margin to the C as to the corner the C leaves open.

| File | Use |
| --- | --- |
| `commitarium-mark.svg` | Mark on light backgrounds (`#171717` C, `#0070f3` hexagon) |
| `commitarium-mark-dark.svg` | Mark on dark backgrounds (`#ededed` C, `#3b8eff` hexagon) |
| `favicon.svg` | Mark that follows the system light or dark setting |
| `app-icon.svg` | App icon: the mark on a black tile, 1024 × 1024 |

The colours are the app's own theme tokens (`--accent` and `--blue` in
`desktop/src/App.css`).

## Changing the mark

Edit `generate.mjs`, then:

```bash
node brand/generate.mjs
```

To rebuild the app icons, render `app-icon.svg` to a 1024 px PNG and run
`pnpm tauri icon <png>` in `desktop/` (delete the `android/` and `ios/`
folders it adds).
