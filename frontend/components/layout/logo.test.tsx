import { render } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { CAT, PAINT } from "@/lib/brand/marks.mjs";

import { Logo } from "./logo";

describe("the logo", () => {
  test("draws every shape of the cat", () => {
    const { container } = render(<Logo />);

    expect(container.querySelector("svg")?.getAttribute("viewBox")).toBe(CAT.viewBox);
    expect(container.querySelectorAll("svg > *")).toHaveLength(CAT.shapes.length);
  });

  test("paints the black and the cream patches from the theme, and the rest in fixed colours", () => {
    const { container } = render(<Logo />);
    const shapes = Array.from(container.querySelectorAll("svg > *"));

    CAT.shapes.forEach(({ paint }, i) => {
      const shape = shapes[i];
      if (paint === "ink") expect(shape.getAttribute("class")).toBe("fill-logo-ink");
      else if (paint === "chalk") expect(shape.getAttribute("class")).toBe("fill-logo-chalk");
      else if (paint === "ink-line") expect(shape.getAttribute("class")).toBe("stroke-logo-ink");
      else expect(shape.getAttribute("fill")).toBe(PAINT[paint as keyof typeof PAINT]);
    });
  });
});
