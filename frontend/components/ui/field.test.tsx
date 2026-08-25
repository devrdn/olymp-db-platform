import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { Field } from "./field";

describe("Field", () => {
  test("keeps an aria attribute the caller set on the control", () => {
    render(
      <Field id="login" label="Login">
        <input name="login" aria-invalid />
      </Field>,
    );

    expect(screen.getByRole("textbox")).toHaveAttribute("aria-invalid", "true");
  });

  test("points the control at its own message", () => {
    render(
      <Field id="login" label="Login" error="Wrong login or password.">
        <input name="login" />
      </Field>,
    );

    const control = screen.getByRole("textbox");
    expect(control).toHaveAttribute("aria-invalid", "true");
    expect(control).toHaveAccessibleDescription("Wrong login or password.");
  });

  /**
   * A sign-in failure belongs to the pair of fields, not to one of them: the
   * API answers `invalid_credentials` without saying which was wrong, on
   * purpose, so the form cannot be used to find out which logins exist. The
   * message is therefore written once, and every field it concerns has to
   * point at it.
   */
  test("marks the control invalid and points it at a message it does not own", () => {
    render(
      <>
        <Field id="login" label="Login" invalid describedBy="sign-in-error">
          <input name="login" />
        </Field>
        <p id="sign-in-error">Wrong login or password.</p>
      </>,
    );

    const control = screen.getByRole("textbox");
    expect(control).toHaveAttribute("aria-invalid", "true");
    expect(control).toHaveAccessibleDescription("Wrong login or password.");
  });

  test("keeps both descriptions when the field has a hint and a form-level message", () => {
    render(
      <>
        <Field id="login" label="Login" hint="Issued by the department." describedBy="sign-in-error">
          <input name="login" />
        </Field>
        <p id="sign-in-error">Wrong login or password.</p>
      </>,
    );

    expect(screen.getByRole("textbox")).toHaveAccessibleDescription(
      "Issued by the department. Wrong login or password.",
    );
  });
});
