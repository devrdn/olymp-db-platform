import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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

/**
 * The client's split: a rule the person needs before they get it wrong stays
 * under the field; why the field exists goes behind a "?" beside its label.
 */
describe("Field, with an explanation behind a question mark", () => {
  const RULE = "Leave empty for unlimited.";
  const WHY = "A participant stuck on a question with no limit has nothing left to move on to.";

  function withHelp(props: { error?: string } = {}) {
    render(
      <Field id="attempts" label="Attempts" hint={RULE} help={WHY} helpLabel="Hint" {...props}>
        <input name="attempts" />
      </Field>,
    );
  }

  test("keeps the rule visible and the explanation closed", () => {
    withHelp();

    expect(screen.getByText(RULE)).toBeVisible();
    expect(screen.getByRole("tooltip", { hidden: true })).not.toBeVisible();
  });

  test("opens the explanation from a named question mark beside the label", async () => {
    const user = userEvent.setup();
    withHelp();

    const trigger = screen.getByRole("button", { name: "Hint" });
    expect(trigger).toHaveAccessibleDescription(WHY);

    await user.click(trigger);
    expect(screen.getByRole("tooltip")).toHaveTextContent(WHY);
  });

  test("leaves the control's name and description exactly what they were", () => {
    withHelp();

    // Beside the label, not inside it: the "?" does not become "Attempts Hint".
    const control = screen.getByRole("textbox", { name: "Attempts" });
    expect(control).toHaveAccessibleName("Attempts");
    expect(control).toHaveAccessibleDescription(RULE);
  });

  test("still lets an error take the rule's slot, and keeps the caller's aria-invalid", () => {
    withHelp({ error: "Must be a whole number." });

    const control = screen.getByRole("textbox", { name: "Attempts" });
    expect(control).toHaveAttribute("aria-invalid", "true");
    expect(control).toHaveAccessibleDescription("Must be a whole number.");
    expect(screen.queryByText(RULE)).toBeNull();
  });
});
