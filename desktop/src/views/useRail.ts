import { useCallback, useEffect, useState } from "react";

function readHidden(key: string): boolean {
  try {
    return localStorage.getItem(key) === "true";
  } catch {
    return false;
  }
}

function useNarrow(width: number): boolean {
  const query = `(max-width: ${width - 1}px)`;
  const [narrow, setNarrow] = useState(() => window.matchMedia(query).matches);
  useEffect(() => {
    const media = window.matchMedia(query);
    const change = () => setNarrow(media.matches);
    change();
    media.addEventListener("change", change);
    return () => media.removeEventListener("change", change);
  }, [query]);
  return narrow;
}

/** Whether a rail is folded: by the user's choice, or by a narrow window
 * unless they opened it anyway. */
export const railHidden = (chosen: boolean, narrow: boolean, opened: boolean) =>
  chosen || (narrow && !opened);

/** A rail of the chat shell. It folds below `foldBelow`, and comes back when
 * the window does. Opening it in a narrow window lasts until the window widens,
 * and only a choice made at full width is remembered. */
export function useRail(key: string, foldBelow: number) {
  const narrow = useNarrow(foldBelow);
  const [chosen, setChosen] = useState(() => readHidden(key));
  const [opened, setOpened] = useState(false);
  useEffect(() => {
    if (!narrow) setOpened(false);
  }, [narrow]);
  const hidden = railHidden(chosen, narrow, opened);

  const toggle = useCallback(() => {
    const next = !hidden;
    if (narrow) setOpened(!next);
    if (narrow && next) return;
    setChosen(next);
    try {
      localStorage.setItem(key, String(next));
    } catch {
      // It lasts until the window closes.
    }
  }, [hidden, key, narrow]);

  return { hidden, toggle };
}
