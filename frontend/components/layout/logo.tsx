import { createElement, type SVGProps } from "react";

import { CAT, LINE_WIDTH, PAINT } from "@/lib/brand/marks.mjs";

/** How a shape is painted: the two theme patches through their tokens, the rest as fixed colours. */
function paintProps(paint: string): SVGProps<SVGElement> {
  if (paint === "ink") return { className: "fill-logo-ink" };
  if (paint === "chalk") return { className: "fill-logo-chalk" };
  if (paint === "ink-line") {
    return { className: "stroke-logo-ink", fill: "none", strokeWidth: LINE_WIDTH, strokeLinecap: "round" };
  }
  return { fill: PAINT[paint as keyof typeof PAINT] };
}

/** The product's logo, the cat on its pedestal (`lib/brand/marks.mjs`), at 32 px high or more. */
export function Logo({ className }: { className?: string }) {
  return (
    <svg data-slot="logo" viewBox={CAT.viewBox} aria-hidden focusable="false" className={className}>
      {CAT.shapes.map(({ el, attrs, paint }, i) => createElement(el, { key: i, ...attrs, ...paintProps(paint) }))}
    </svg>
  );
}
