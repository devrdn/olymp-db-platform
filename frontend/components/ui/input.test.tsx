import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, test } from "vitest";

import { Input } from "./input";

/** The import guard for this component is in `button.test.tsx`. */
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

  // The invalid border is a selector on the DOM attribute, so the attribute
  // must reach the element.
  test("marks itself invalid where it is told to", () => {
    render(<Input aria-label="Login" aria-invalid />);

    expect(screen.getByLabelText("Login")).toHaveAttribute("aria-invalid", "true");
  });
});
