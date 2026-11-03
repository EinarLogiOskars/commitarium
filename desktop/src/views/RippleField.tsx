import { useEffect, useRef } from "react";

// Behind an app that is being made or started: a grid of faint light-blue
// dots, and a ripple leaving the centre about once a second, like drops
// landing in water. Where a ripple passes, the dots brighten and swell; each
// ripple weakens as it spreads, so a few show at once at different strengths.
// Behind the words beside it (its siblings) the field stays quiet, so the
// text reads; the ripple shows again just outside them.
// With reduced motion, the dots hold still.

const SPACING = 26;
const EVERY_MS = 1000;
/** How fast a ripple spreads, in pixels a second, and how long it lasts. */
const SPEED = 150;
const LIFE_MS = 3600;
/** How wide a ripple's ring is. */
const WIDTH = 20;
const COLOR = "125, 211, 252";
/** How far beyond the words the field takes to come back to full strength. */
const QUIET_EDGE = 56;

/** The area the canvas' sibling elements cover, in the canvas' coordinates. */
function wordsArea(canvas: HTMLCanvasElement) {
  const box = canvas.getBoundingClientRect();
  let left = Infinity;
  let top = Infinity;
  let right = -Infinity;
  let bottom = -Infinity;
  for (const sibling of Array.from(canvas.parentElement?.children ?? [])) {
    if (sibling === canvas) continue;
    const r = sibling.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) continue;
    left = Math.min(left, r.left - box.left);
    top = Math.min(top, r.top - box.top);
    right = Math.max(right, r.right - box.left);
    bottom = Math.max(bottom, r.bottom - box.top);
  }
  return left === Infinity ? null : { left, top, right, bottom };
}

export function RippleField() {
  const canvas = useRef<HTMLCanvasElement | null>(null);

  useEffect(() => {
    const el = canvas.current;
    const context = el?.getContext("2d");
    if (!el || !context) return;
    const still = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    let frame = 0;
    const started = performance.now();

    const draw = (now: number) => {
      const scale = window.devicePixelRatio || 1;
      const width = el.clientWidth;
      const height = el.clientHeight;
      if (el.width !== Math.round(width * scale) || el.height !== Math.round(height * scale)) {
        el.width = Math.round(width * scale);
        el.height = Math.round(height * scale);
      }
      context.setTransform(scale, 0, 0, scale, 0, 0);
      context.clearRect(0, 0, width, height);
      const cx = width / 2;
      const cy = height / 2;
      const words = wordsArea(el);
      const elapsed = now - started;
      // The ripples alive now: how far each has gone, and how strong it is.
      const ripples: { radius: number; strength: number }[] = [];
      if (!still) {
        for (let age = elapsed % EVERY_MS; age < LIFE_MS && age <= elapsed; age += EVERY_MS) {
          const left = 1 - age / LIFE_MS;
          ripples.push({ radius: (SPEED * age) / 1000, strength: left * left });
        }
      }
      const columns = Math.ceil(width / SPACING / 2) + 1;
      const rows = Math.ceil(height / SPACING / 2) + 1;
      for (let row = -rows; row <= rows; row++) {
        for (let column = -columns; column <= columns; column++) {
          const x = cx + column * SPACING;
          const y = cy + row * SPACING;
          const distance = Math.hypot(x - cx, y - cy);
          let glow = 0;
          for (const ripple of ripples) {
            const off = (distance - ripple.radius) / WIDTH;
            glow += ripple.strength * Math.exp(-off * off);
          }
          // 0 behind the words, rising to 1 a short way outside them.
          let open = 1;
          if (words) {
            const dx = Math.max(words.left - x, 0, x - words.right);
            const dy = Math.max(words.top - y, 0, y - words.bottom);
            const t = Math.min(1, Math.hypot(dx, dy) / QUIET_EDGE);
            open = t * t * (3 - 2 * t);
          }
          glow = Math.min(1, glow) * open;
          const radius = 1.1 + 3 * glow;
          if (glow > 0.05) {
            context.fillStyle = `rgba(${COLOR}, ${0.3 * glow})`;
            context.beginPath();
            context.arc(x, y, radius * 3.4, 0, Math.PI * 2);
            context.fill();
          }
          context.fillStyle = `rgba(${COLOR}, ${0.04 + 0.06 * open + 0.8 * glow})`;
          context.beginPath();
          context.arc(x, y, radius, 0, Math.PI * 2);
          context.fill();
        }
      }
      if (!still) frame = requestAnimationFrame(draw);
    };
    frame = requestAnimationFrame(draw);
    return () => cancelAnimationFrame(frame);
  }, []);

  return <canvas ref={canvas} className="ripple-field" aria-hidden />;
}
