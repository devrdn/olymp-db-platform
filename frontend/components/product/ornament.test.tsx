import { render } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { OrnamentBand, OrnamentStar } from "./ornament";

describe("the ornament band", () => {
  test("the band repeats its motif and says nothing to a screen reader", () => {
    const { container } = render(<OrnamentBand repeats={8} />);

    const svg = container.querySelector("svg");
    expect(svg).toHaveAttribute("aria-hidden", "true");
    expect(container.querySelectorAll("[data-motif]")).toHaveLength(8);
  });

  test("its strokes come from tokens, never from a literal colour", () => {
    const { container } = render(<OrnamentBand repeats={2} />);
    expect(container.innerHTML).not.toMatch(/#[0-9a-f]{3,6}/i);
  });

  /**
   * The geometry is a cross-stitch grid, and a grid squeezed horizontally is
   * no longer a grid: the rhombus becomes a lozenge and the river's forty-five
   * degrees become something else. The band is cut off at the edge of the
   * content column instead, which is what `slice` does — `none` is what would
   * squeeze it, and the SVG default `meet` would shrink the pattern and
   * letterbox the band rather than fill it.
   */
  test("grows by repeating rather than by stretching one motif", () => {
    const { container } = render(<OrnamentBand repeats={12} />);
    const svg = container.querySelector("svg");

    expect(svg).toHaveAttribute("viewBox", "0 0 288 24");
    expect(svg).toHaveAttribute("preserveAspectRatio", "xMinYMid slice");
  });

  /**
   * `slice` crops whatever the box cannot hold, and when the tiles run out
   * before the band's pixel width does, what it crops is the height: the
   * pattern is scaled up and the two rows of rivers go over the top and bottom
   * edges, leaving a line of bare rhombi. The default has to out-measure the
   * widest content column the system has — `--container-column`, 110rem —
   * since the band is drawn at 24 px high and the tile is 24 units square.
   */
  test("its default reaches past the widest content column", () => {
    const { container } = render(<OrnamentBand />);
    const [, , width] = container.querySelector("svg")!.getAttribute("viewBox")!.split(" ");

    expect(Number(width)).toBeGreaterThanOrEqual(110 * 16);
  });

  /** One ornament, one colour: a band mixing the three is not a pattern. */
  test("carries one colour of the sub-palette, and the caller may pick another", () => {
    const { container } = render(<OrnamentBand repeats={2} />);
    expect(container.querySelector("svg")).toHaveClass("text-ornament-indigo");

    const footer = render(<OrnamentBand repeats={2} className="text-ornament-walnut" />);
    const band = footer.container.querySelector("svg");
    expect(band).toHaveClass("text-ornament-walnut");
    expect(band).not.toHaveClass("text-ornament-indigo");
  });
});

describe("the ornament star", () => {
  test("is hidden from a screen reader and sized like a lowercase letter", () => {
    const { container } = render(<OrnamentStar />);

    const svg = container.querySelector("svg");
    expect(svg).toHaveAttribute("aria-hidden", "true");
    expect(svg).toHaveAttribute("viewBox", "0 0 24 24");
    expect(svg).toHaveClass("h-[0.72em]", "w-[0.72em]", "text-ornament-madder");
  });

  test("is drawn from two squares, in tokens rather than in a literal colour", () => {
    const { container } = render(<OrnamentStar />);

    expect(container.querySelectorAll("path")).toHaveLength(2);
    expect(container.innerHTML).not.toMatch(/#[0-9a-f]{3,6}/i);
  });
});
