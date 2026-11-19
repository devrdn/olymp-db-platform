import { render, screen } from "@testing-library/react";
import { describe, test } from "vitest";

import { Checkbox } from "./checkbox";
import { expectTouchTarget } from "./touch-target-test-support";

describe("Checkbox", () => {
  test("takes a finger-sized press around the box on a coarse pointer, leaving the box its size", () => {
    render(<Checkbox aria-label="Pick" />);

    expectTouchTarget(
      screen.getByRole("checkbox"),
      "relative",
      "size-4",
      "pointer-coarse:after:absolute",
      "pointer-coarse:after:-inset-4",
    );
  });
});
