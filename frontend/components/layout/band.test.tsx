import { render } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { Band } from "./band";

describe("a band", () => {
  test("draws the rule that separates it from the next one", () => {
    const { container } = render(<Band>content</Band>);

    expect(container.querySelector("section")?.className).toContain("border-b");
  });

  // The last band on a page has nothing after it. This is a prop rather than
  // a class because `className` lands on the content column: passing
  // `border-b-0` looks like it works, does nothing, and takes a browser to
  // notice.
  test("can be told not to, and that reaches the element that draws it", () => {
    const { container } = render(<Band rule={false}>content</Band>);

    const section = container.querySelector("section");
    expect(section?.className).not.toContain("border-b");
  });

  test("puts the caller's classes on the content column, not on the section", () => {
    const { container } = render(<Band className="gap-8">content</Band>);

    expect(container.querySelector("section")?.className).not.toContain("gap-8");
    expect(container.innerHTML).toContain("gap-8");
  });
});
