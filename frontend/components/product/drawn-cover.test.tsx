import { render } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { DrawnCover } from "./drawn-cover";

/**
 * Determinism matters most: a cover that changed between loads would make a
 * contest look different on every visit.
 */
describe("the drawn cover", () => {
  test("draws one contest the same way twice", () => {
    const first = render(<DrawnCover seed="0198c3a0-6d1e-7c8b-9f22-2b6b1f0c4a11" />);
    const second = render(<DrawnCover seed="0198c3a0-6d1e-7c8b-9f22-2b6b1f0c4a11" />);

    expect(first.container.innerHTML).toBe(second.container.innerHTML);
  });

  test("draws two contests differently", () => {
    const one = render(<DrawnCover seed="0198c3a0-6d1e-7c8b-9f22-2b6b1f0c4a11" />);
    const other = render(<DrawnCover seed="0198c3a0-6d1e-7c8b-9f22-2b6b1f0c4a12" />);

    expect(one.container.innerHTML).not.toBe(other.container.innerHTML);
  });

  /**
   * UUIDv7s created together differ only in their last characters; a hash that
   * collapsed such runs would give every contest the same picture.
   */
  test("spreads a run of neighbouring identifiers over several drawings", () => {
    const drawings = new Set(
      Array.from({ length: 24 }, (_, index) => {
        const { container } = render(
          <DrawnCover seed={`0198c3a0-6d1e-7c8b-9f22-2b6b1f0c4a${index.toString(16).padStart(2, "0")}`} />,
        );
        return container.innerHTML;
      }),
    );

    expect(drawings.size).toBeGreaterThan(20);
  });

  /**
   * ESLint catches a colour literal in a class name; this catches one in an SVG
   * attribute, which would break the dark theme.
   */
  test("writes no colour of its own", () => {
    const { container } = render(<DrawnCover seed="0198c3a0-6d1e-7c8b-9f22-2b6b1f0c4a11" />);

    expect(container.innerHTML).not.toMatch(/#[0-9a-f]{3}|rgba?\(|hsla?\(|oklch\(|lab\(|color-mix\(/i);
  });

  /** Unnamed by default, since a card prints the title over it; named where it stands alone. */
  test("is decoration unless it is asked to name itself", () => {
    const { container } = render(<DrawnCover seed="a" />);
    expect(container.firstElementChild).toHaveAttribute("aria-hidden");
    expect(container.querySelector('[role="img"]')).toBeNull();

    const named = render(<DrawnCover seed="a" label="Drawn cover" />);
    expect(named.getByRole("img", { name: "Drawn cover" })).toBeInTheDocument();
  });
});
