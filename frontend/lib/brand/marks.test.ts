import { describe, expect, test } from "vitest";

import { CAT, LINE_WIDTH, PAINT, markSvg } from "./marks.mjs";

const scheme = { ink: "#111111", chalk: "#eeeeee" };

/** The svg's shapes as the browser would parse them. */
function shapes(svg: string): Element[] {
  const doc = new DOMParser().parseFromString(svg, "image/svg+xml");
  expect(doc.querySelector("parsererror")).toBeNull();
  return Array.from(doc.documentElement.children);
}

describe("a mark as an SVG document", () => {
  test("keeps the mark's box and every shape, in order", () => {
    const svg = markSvg(CAT, scheme);
    const doc = new DOMParser().parseFromString(svg, "image/svg+xml");

    expect(doc.documentElement.getAttribute("viewBox")).toBe(CAT.viewBox);
    expect(shapes(svg).map((shape) => shape.tagName)).toEqual(CAT.shapes.map((shape) => shape.el));
  });

  test("paints the two theme patches in the scheme given, and the rest in their fixed colours", () => {
    shapes(markSvg(CAT, scheme)).forEach((shape, i) => {
      const { paint } = CAT.shapes[i];
      if (paint === "ink-line") {
        expect([shape.getAttribute("fill"), shape.getAttribute("stroke")]).toEqual(["none", scheme.ink]);
        expect(shape.getAttribute("stroke-width")).toBe(String(LINE_WIDTH));
      } else {
        const expected = paint === "ink" ? scheme.ink : paint === "chalk" ? scheme.chalk : PAINT[paint as keyof typeof PAINT];
        expect(shape.getAttribute("fill")).toBe(expected);
      }
    });
  });
});
