import { useEffect, useRef } from "react";

// Behind an app that is being made or started: a grid of small holographic
// spheres on a still surface, and drops landing in its centre. Each drop sends
// out a wave as water does: it bursts out fast and slows as it spreads, and it
// is a short train, not one ring: a crest, a dip, a smaller crest, so it
// bounces as it goes, and it widens and weakens with distance. The spheres
// stay where they are, unseen at rest: one fades in on a crest, swelling a
// little at its peak, and out again in the dip. A drop lands at a steady beat.
// Behind the words beside it (its siblings) the field stays quiet, so the
// text reads; the waves show again just outside them.
// The colour comes from CSS (color on .ripple-field), so it follows the theme.
// With reduced motion, the spheres hold still.

const SPACING = 26;
/** How long a drop's wave lives, in seconds. */
const LIFE = 5;
/** How far a wave would spread, as a share of the field's larger side, and
 * how quickly it slows on the way (seconds). */
const REACH = 0.8;
const SLOWING = 1.6;
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

/** The beat a drop lands at, in seconds. */
const EVERY = 1.6;

/** How far the surface rises (up to about 1) or dips (below 0) at distance d
 * from the centre, from a drop that landed age seconds ago. */
function wave(d: number, age: number, size: number) {
  const front = size * REACH * (1 - Math.exp(-age / SLOWING));
  const length = 40 + 16 * age;
  const behind = front - d;
  if (behind < -length) return 0;
  const envelope =
    behind < 0 ? Math.exp(-((behind / (0.35 * length)) ** 2)) : Math.exp(-behind / (2 * length));
  const fade = age > LIFE - 1 ? LIFE - age : 1;
  return Math.exp(-age / 1.8) * fade * envelope * Math.cos((2 * Math.PI * behind) / length);
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
    // When each drop landed (seconds), and when the next one lands.
    let drops: number[] = [];
    let next = 0;

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
      const color = getComputedStyle(el).color;
      const tintOf = (alpha: number) =>
        color.replace(/^rgb\((.+)\)$/, `rgba($1, ${Math.max(0, Math.min(1, alpha))})`);
      const cx = width / 2;
      const cy = height / 2;
      const size = Math.max(width, height);
      const words = wordsArea(el);
      const t = (now - started) / 1000;
      if (!still) {
        while (next <= t) {
          drops.push(next);
          next += EVERY;
        }
        drops = drops.filter((at) => t - at < LIFE);
      }

      const columns = Math.ceil(width / SPACING / 2) + 1;
      const rows = Math.ceil(height / SPACING / 2) + 1;
      for (let row = -rows; row <= rows; row++) {
        for (let column = -columns; column <= columns; column++) {
          const x = cx + column * SPACING;
          const y = cy + row * SPACING;
          const d = Math.hypot(x - cx, y - cy);
          let rise = 0;
          for (const at of drops) rise += wave(d, t - at, size);
          // 0 behind the words, rising to 1 a short way outside them.
          let open = 1;
          if (words) {
            const dx = Math.max(words.left - x, 0, x - words.right);
            const dy = Math.max(words.top - y, 0, y - words.bottom);
            const s = Math.min(1, Math.hypot(dx, dy) / QUIET_EDGE);
            open = s * s * (3 - 2 * s);
          }
          rise = Math.max(-1, Math.min(1.3, rise)) * open;
          // The grid is there all along, unseen: a sphere fades in on a
          // crest and out again, swelling a little at its peak.
          const up = Math.min(1, Math.max(0, rise));
          if (up < 0.03) continue;
          const r = 4 + 1.5 * up;
          const tint = (alpha: number) => tintOf(alpha * up);

          // A holographic sphere: a glassy body, a clear outline, a meridian
          // and an equator so it reads as a globe, and a highlight.
          context.fillStyle = tint(0.14);
          context.beginPath();
          context.arc(x, y, r, 0, Math.PI * 2);
          context.fill();
          context.lineWidth = 0.9;
          context.strokeStyle = tint(0.95);
          context.stroke();
          context.lineWidth = 0.6;
          context.strokeStyle = tint(0.6);
          context.beginPath();
          context.ellipse(x, y, r * 0.42, r, 0, 0, Math.PI * 2);
          context.stroke();
          context.beginPath();
          context.ellipse(x, y, r, r * 0.32, 0, 0, Math.PI * 2);
          context.stroke();
          context.fillStyle = tint(0.85);
          context.beginPath();
          context.arc(x - r * 0.38, y - r * 0.42, r * 0.17, 0, Math.PI * 2);
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
