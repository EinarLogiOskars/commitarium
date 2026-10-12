// The app's appearance: light (the default), dark, or following the system.
// It is this machine's preference, so it lives in local storage, and it is
// applied before the first render so the app never flashes the wrong theme.

export type Theme = "light" | "dark" | "system";

const KEY = "commitarium.theme";

export function readTheme(): Theme {
  try {
    const stored = localStorage.getItem(KEY);
    if (stored === "light" || stored === "dark" || stored === "system") return stored;
  } catch {
    // Unavailable storage keeps the default.
  }
  return "light";
}

export function applyTheme(theme: Theme) {
  document.documentElement.dataset.theme = theme;
}

export function saveTheme(theme: Theme) {
  applyTheme(theme);
  try {
    localStorage.setItem(KEY, theme);
  } catch {
    // The choice lasts until the window closes.
  }
}
