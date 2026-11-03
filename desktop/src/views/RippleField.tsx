import { useEffect, useRef } from "react";

// Behind an app that is being made or started: a grid of faint light-blue
// dots, and a ripple leaving the centre about once a second, like drops
// landing in water. Where a ripple passes, the dots brighten and swell; each
// ripple weakens as it spreads, so a few show at once at different strengths.
// With reduced motion, the dots hold still.

const SPACING = 26;
const EVERY_MS = 1000;
/** How fast a ripple spreads, in pixels a second, and how long it lasts. */
const SPEED = 150;
const LIFE_MS = 3600;
/** How wide a ripple's ring is. */
const WIDTH = 20;
const COLOR = "125, 211, 252";

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
          glow = Math.min(1, glow);
          const radius = 1.1 + 3 * glow;
          if (glow > 0.05) {
            context.fillStyle = `rgba(${COLOR}, ${0.3 * glow})`;
            context.beginPath();
            context.arc(x, y, radius * 3.4, 0, Math.PI * 2);
            context.fill();
          }
          context.fillStyle = `rgba(${COLOR}, ${0.1 + 0.8 * glow})`;
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
