import { render } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { DrawnCover } from "./drawn-cover";

/**
 * The drawn cover is a cover, not a placeholder, and these are the three
 * promises that let a mixed row of photographs and drawings read as one row.
 *
 * Determinism is the first of them and the one that would be missed latest: a
 * cover that changed between two loads of the same page would make an
 * olympiad look like a different olympiad every time somebody came back to it,
 * and nothing about it would look deliberate.
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
   * The geometry is a hash of the identifier, and a hash that collapsed to one
   * value for a run of similar identifiers would be deterministic and useless:
   * a whole installation's covers would be the same picture. Identifiers
   * really do arrive in runs — UUIDv7 is written in creation order, and the
   * contests of one day differ in their last characters.
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
   * Every colour comes from styles/tokens.css, which is what keeps the drawn
   * cover in the same theme as the photograph beside it — in the dark theme
   * as well, where a literal would be a bright rectangle in a dark row.
   * ESLint catches a literal in a class name; this catches one written into
   * an attribute, where the drawing's own geometry lives.
   */
  test("writes no colour of its own", () => {
    const { container } = render(<DrawnCover seed="0198c3a0-6d1e-7c8b-9f22-2b6b1f0c4a11" />);

    expect(container.innerHTML).not.toMatch(/#[0-9a-f]{3}|rgba?\(|hsla?\(|oklch\(|lab\(|color-mix\(/i);
  });

  /**
   * It carries no name by default: on a card the contest's title is printed
   * over it, and a picture that says nothing the page does not already say is
   * decoration. Where it stands alone — the organiser's own panel — it is
   * given one.
   */
  test("is decoration unless it is asked to name itself", () => {
    const { container } = render(<DrawnCover seed="a" />);
    expect(container.firstElementChild).toHaveAttribute("aria-hidden");
    expect(container.querySelector('[role="img"]')).toBeNull();

    const named = render(<DrawnCover seed="a" label="Drawn cover" />);
    expect(named.getByRole("img", { name: "Drawn cover" })).toBeInTheDocument();
  });
});
