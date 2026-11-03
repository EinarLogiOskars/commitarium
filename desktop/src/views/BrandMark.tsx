/**
 * The Commitarium mark: a hexagon C holding a blue hexagon. Drawn inline so it
 * takes the theme's colours (--accent for the C, --blue for the hexagon) and
 * follows light and dark. Sources live in brand/.
 */
export function BrandMark({ size = 18, className }: { size?: number; className?: string }) {
  return (
    <svg
      className={className}
      viewBox="-200 -173.21 368 346.41"
      height={size}
      width={(size * 368) / 346.41}
      aria-hidden
      focusable="false"
    >
      <polygon
        fill="var(--accent, #ededed)"
        points="150,86.6 100,173.21 -100,173.21 -200,0 -100,-173.21 100,-173.21 150,-86.6 50,-86.6 -50,-86.6 -100,0 -50,86.6 50,86.6"
      />
      <polygon
        fill="var(--blue, #3b8eff)"
        points="168,0 134,58.89 66,58.89 32,0 66,-58.89 134,-58.89"
      />
    </svg>
  );
}
