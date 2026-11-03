import { useEffect, useState, type ReactNode } from "react";
import { AbsoluteFill, continueRender, delayRender, getInputProps } from "remotion";
import { missed } from "./world";

export type Theme = "light" | "dark";

// One docs screenshot: the app's tokens for the theme, and the still taken
// once the view has fetched its demo data and settled.
// Clicks follow one another as a person's would, each on the element whose
// text or accessible name is the label, once the view has loaded.
const STEP_MS = 900;

function clickLabel(label: string) {
  // "choose:Option" picks an option of a native select, as React hears it.
  if (label.startsWith("choose:")) {
    const text = label.slice("choose:".length);
    const select = [...document.querySelectorAll<HTMLSelectElement>("select")].find((el) =>
      [...el.options].some((option) => option.textContent?.trim() === text),
    );
    const option = select && [...select.options].find((o) => o.textContent?.trim() === text);
    if (!select || !option) {
      missedClicks.add(label);
      return;
    }
    Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")?.set?.call(select, option.value);
    select.dispatchEvent(new Event("change", { bubbles: true }));
    return;
  }
  // "scroll:Heading" brings a section into view instead of clicking.
  if (label.startsWith("scroll:")) {
    const text = label.slice("scroll:".length);
    const heading = [...document.querySelectorAll<HTMLElement>("h1, h2, h3, h4, strong, legend")].find(
      (el) => el.textContent?.trim().toLowerCase() === text.toLowerCase(),
    );
    heading?.scrollIntoView({ block: "start" });
    if (!heading) missedClicks.add(label);
    return;
  }
  // Latest first: an open dialog comes after the page beneath it.
  const candidates = [
    ...document.querySelectorAll<HTMLElement>("button, a, [role=menuitem], [role=tab], summary, label"),
  ].reverse();
  const target =
    candidates.find((el) => el.textContent?.trim() === label) ??
    candidates.find((el) => el.getAttribute("aria-label") === label) ??
    candidates.find((el) => el.textContent?.trim().startsWith(label));
  target?.click();
  if (!target) missedClicks.add(label);
  else if (getInputProps().debug)
    missedClicks.add(`${Math.round(performance.now())}ms ${label} -> ${target.tagName}.${target.className}`);
}
const missedClicks = new Set<string>();

export function Screen({
  theme,
  children,
  steps = [],
  settle = 700,
}: {
  theme: Theme;
  children: ReactNode;
  /** Labels to click, in order, before the still is taken. */
  steps?: string[];
  settle?: number;
}) {
  if (typeof document !== "undefined") document.documentElement.dataset.theme = theme;
  const total = settle + steps.length * STEP_MS;
  const [handle] = useState(() => delayRender("the view settles"));
  useEffect(() => {
    const timers = steps.map((label, index) => setTimeout(() => clickLabel(label), settle + index * STEP_MS));
    timers.push(
      setTimeout(() => {
        if (getInputProps().debug) missedClicks.add(`${Math.round(performance.now())}ms still`);
        // No focus ring from the last click in the picture.
        (document.activeElement as HTMLElement | null)?.blur?.();
        requestAnimationFrame(() => continueRender(handle));
      }, total + 300),
    );
    return () => timers.forEach(clearTimeout);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [handle]);
  // Rendered with --props='{"debug":true}', the still lists what the demo
  // world did not answer.
  const debug = !!getInputProps().debug;
  const [shown, setShown] = useState<string[]>([]);
  useEffect(() => {
    if (!debug) return;
    const timer = setInterval(() => setShown([...missed, ...[...missedClicks].map((l) => `click ${l}`)]), 100);
    return () => clearInterval(timer);
  }, [debug]);
  return (
    <AbsoluteFill style={{ background: "var(--bg)", color: "var(--text)" }}>
      {children}
      {debug && shown.length > 0 && (
        <pre style={{ position: "absolute", left: 8, bottom: 8, zIndex: 9999, margin: 0, padding: 8, background: "#300", color: "#fcc", font: "11px monospace" }}>
          {shown.join("\n")}
        </pre>
      )}
    </AbsoluteFill>
  );
}
