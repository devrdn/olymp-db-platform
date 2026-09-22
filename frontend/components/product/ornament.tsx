import { cn } from "@/lib/utils";

/**
 * The ornament: Moldovan embroidery geometry, drawn in the interface's own
 * hairlines.
 *
 * The motifs of the folk shirt and the carpet — the rhombus, the zig-zag the
 * weavers call *râuri* ("rivers"), the eight-pointed star — are all built on
 * the square grid of a cross stitch. That is the same nature as an interface
 * made entirely of rules, which is why the culture enters as geometry and not
 * as a photograph: one parametric SVG, a grid unit, a stroke width, a number
 * of repeats.
 *
 * Three things hold it to that:
 *
 * - **It is decorative and says nothing.** Every shape here is `aria-hidden`,
 *   carries no title and is never laid under text. A reader who cannot see it
 *   has lost nothing, so it is held to no text-contrast threshold either.
 * - **One ornament, one colour.** The colour is set once on the element and
 *   the strokes follow it through `currentColor`, so the theme switches the
 *   ornament without a single path being repainted. Mixing the three dyes of
 *   the sub-palette inside one band turns a pattern into a motley.
 * - **It is the frame of the landing page, not its content.** A band under
 *   the hero, a narrow band in the footer, a star in the hero. It does not
 *   spread onto the working screens.
 *
 * The colours come from the "ornament" sub-palette in `styles/tokens.css`
 * (SPEC section 3.6): the band under the hero is indigo, the footer's band is
 * walnut, the star is madder.
 */

/**
 * One motif: a rhombus between two runs of the river zig-zag.
 *
 * Twenty-four units wide and twenty-four tall, on a grid whose cell is eight —
 * the cross stitch the pattern is counted in. The rhombus is drawn from the
 * midpoints of the middle cell, so every line in the tile runs at forty-five
 * degrees and the whole thing stays a counted pattern rather than a drawing.
 */
function Motif({ x }: { x: number }) {
  return (
    <g data-motif transform={`translate(${x} 0)`}>
      {/* râuri — the river, a zig-zag across one row of the grid */}
      <path d="M0 4 L4 0 L8 4 L12 0 L16 4 L20 0 L24 4" />
      {/* romb — the rhombus, from the midpoints of a cell */}
      <path d="M8 12 L12 8 L16 12 L12 16 Z" />
      <path d="M0 20 L4 24 L8 20 L12 24 L16 20 L20 24 L24 20" />
    </g>
  );
}

/**
 * A horizontal band of the ornament, the width of the content column.
 *
 * The band grows by repeating the tile, not by stretching one of them:
 * `preserveAspectRatio="xMinYMid slice"` cuts the pattern off at the edge of
 * the column the way a woven border is cut off at the edge of the cloth.
 *
 * Neither of the alternatives is that. `preserveAspectRatio="none"` is the
 * one that squeezes, and a cross-stitch grid squeezed along one axis is no
 * longer a cross-stitch grid: the rhombus flattens into a lozenge and the
 * rivers lose their forty-five degrees. The SVG default, `meet`, keeps the
 * proportions but fits the whole viewBox inside the band — it would shrink
 * the pattern and letterbox what is left, which is a thin ornament floating
 * in a thick empty rule.
 *
 * `repeats` therefore buys reach rather than width, and running out of tiles
 * is not a gap at the right edge — it is the moment `slice` starts zooming.
 * The tile is 24 units wide and 24 tall and the band is drawn 24 px high, so
 * the pattern is at its natural size exactly while the viewBox is at least as
 * many units as the band is pixels wide; past that, `slice` scales the
 * geometry up and crops the rivers off the top and bottom rows, leaving a line
 * of bare rhombi. The default covers 1920 px, which is wider than the widest
 * content column the system has (`--container-column`, 110rem).
 */
export function OrnamentBand({
  repeats = 80,
  className,
}: {
  repeats?: number;
  className?: string;
}) {
  return (
    <svg
      aria-hidden="true"
      viewBox={`0 0 ${repeats * 24} 24`}
      preserveAspectRatio="xMinYMid slice"
      fill="none"
      stroke="currentColor"
      strokeWidth={1}
      className={cn("h-6 w-full text-ornament-indigo", className)}
    >
      {Array.from({ length: repeats }, (_, i) => (
        <Motif key={i} x={i * 24} />
      ))}
    </svg>
  );
}

/**
 * The eight-pointed star, as a mark beside the installation's name.
 *
 * Two squares over one centre — one on the grid's axes, one turned by
 * forty-five degrees — which is how the star is counted in the stitch and why
 * it needs no curve to draw. It is set at the size of a lowercase letter and
 * carries no space of its own, so it reads as a mark in the line rather than
 * as an illustration beside it.
 *
 * Its stroke is two units where the band's is one: the band is drawn at its
 * own height of 24 px, while this box is scaled down to about twelve, so two
 * units here and one unit there arrive on screen as the same hairline.
 */
export function OrnamentStar({ className }: { className?: string }) {
  return (
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      className={cn("inline-block h-[0.72em] w-[0.72em] text-ornament-madder", className)}
    >
      {/* The square on the grid's own axes. */}
      <path d="M5 5 H19 V19 H5 Z" />
      {/* The same square turned by forty-five degrees. */}
      <path d="M12 2 L22 12 L12 22 L2 12 Z" />
    </svg>
  );
}
