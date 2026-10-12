import { useEffect, useState } from "react";

// Widths, in CSS pixels, that decide how the open assistant fits.
const RAIL = 264;
const RAIL_COLLAPSED = 52;
const PAGE_MIN = 720;
const RAIL_KEY = "commitarium.rail.collapsed";

/** The assistant's docked width: 40% of the window, kept to 420–640px. */
function assistantWidth(windowWidth: number) {
  return Math.round(Math.min(Math.max(windowWidth * 0.4, 420), 640));
}

function readCollapsed() {
  try {
    return localStorage.getItem(RAIL_KEY) === "1";
  } catch {
    return false;
  }
}

// How the rail and the assistant share the window. The rail collapses when
// the user asks. An open assistant docks beside the page when the page keeps
// enough room; to make that room it first collapses the rail, and only when
// even that is not enough does it lie over the page. Expanding the rail while
// the assistant holds it collapsed keeps the rail and sends the assistant
// over the page, until the assistant closes.
export function useWorkLayout(hasRail: boolean) {
  const [windowWidth, setWindowWidth] = useState(() => window.innerWidth);
  const [assistantOpen, setAssistantOpen] = useState(false);
  const [userCollapsed, setUserCollapsed] = useState(readCollapsed);
  const [railKept, setRailKept] = useState(false);

  useEffect(() => {
    const onResize = () => setWindowWidth(window.innerWidth);
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, []);

  const width = assistantWidth(windowWidth);
  const rail = hasRail ? RAIL : 0;
  const collapsedRail = hasRail ? RAIL_COLLAPSED : 0;
  let railCollapsed = userCollapsed;
  let docked = false;
  if (assistantOpen) {
    if (!userCollapsed && windowWidth >= rail + PAGE_MIN + width) {
      docked = true;
    } else if (!railKept && windowWidth >= collapsedRail + PAGE_MIN + width) {
      docked = true;
      railCollapsed = true;
    }
  }

  const toggleRail = () => {
    if (railCollapsed) {
      // The assistant may be what holds it collapsed.
      if (assistantOpen) setRailKept(true);
      save(false);
    } else {
      setRailKept(false);
      save(true);
    }
  };
  const save = (collapsed: boolean) => {
    setUserCollapsed(collapsed);
    try {
      localStorage.setItem(RAIL_KEY, collapsed ? "1" : "0");
    } catch {
      // The choice lasts until the window closes.
    }
  };

  const openAssistant = (open: boolean) => {
    setAssistantOpen(open);
    if (!open) setRailKept(false);
  };

  return {
    assistantOpen,
    setAssistantOpen: openAssistant,
    docked,
    assistantWidth: width,
    railCollapsed,
    toggleRail,
  };
}
