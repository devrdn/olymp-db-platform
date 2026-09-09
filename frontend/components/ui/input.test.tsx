import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test } from "vitest";

import { Input } from "./input";

/**
 * The import guard for this file lives beside the button's
 * (`button.test.tsx`): the two were the same finding and share one
 * assertion, so a future edit that reaches back for a primitive fails in one
 * place rather than in two that can drift.
 */
describe("the field", () => {
  test("is a native input, so a form reads it the ordinary way", async () => {
    render(
      <form>
        <label htmlFor="login">Login</label>
        <Input id="login" name="login" defaultValue="" />
      </form>,
    );

    const field = screen.getByLabelText("Login");
    expect(field.tagName).toBe("INPUT");

    await userEvent.type(field, "margot");
    expect(new FormData(field.closest("form") as HTMLFormElement).get("login")).toBe("margot");
  });

  test("passes its type through rather than deciding one", () => {
    render(<Input type="password" aria-label="Password" />);

    expect(screen.getByLabelText("Password")).toHaveAttribute("type", "password");
  });

  // The invalid border is a Tailwind selector on the DOM attribute, so the
  // attribute has to reach the element for it to mean anything.
  test("marks itself invalid where it is told to", () => {
    render(<Input aria-label="Login" aria-invalid />);

    expect(screen.getByLabelText("Login")).toHaveAttribute("aria-invalid", "true");
  });
});
