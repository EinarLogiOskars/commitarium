import { useEffect, useRef } from "react";
import { drawRipple, type Area } from "./rippleDraw";

// Behind an app that is being made or started: drops landing in water, as
// holographic spheres (rippleDraw.ts). The colour comes from CSS (color on
// .ripple-field), so it follows the theme. With reduced motion, it holds
// still.

/** The area the canvas' sibling elements cover, in the canvas' coordinates. */
function wordsArea(canvas: HTMLCanvasElement): Area | null {
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
      // Held still, a wave stands frozen part way out, so the spheres show.
      const t = still ? 1.2 : (now - started) / 1000;
      drawRipple(context, width, height, t, getComputedStyle(el).color, wordsArea(el));
      if (!still) frame = requestAnimationFrame(draw);
    };
    frame = requestAnimationFrame(draw);
    return () => cancelAnimationFrame(frame);
  }, []);

  return <canvas ref={canvas} className="ripple-field" aria-hidden />;
}
