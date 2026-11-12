import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { InlineLink } from "./inline-link";

describe("the inline link", () => {
  test("is a link to where it says, named by its text", () => {
    render(<InlineLink href="/forgot">Forgot password?</InlineLink>);

    expect(screen.getByRole("link", { name: "Forgot password?" })).toHaveAttribute("href", "/forgot");
  });

  /** WCAG 1.4.1: a link must not be told apart by colour alone. */
  test("is underlined", () => {
    render(<InlineLink href="/signup">Create an account</InlineLink>);

    expect(screen.getByRole("link")).toHaveClass("underline");
  });
});
