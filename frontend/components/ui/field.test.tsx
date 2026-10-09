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
   * The API does not say which sign-in field was wrong, so logins cannot be
   * probed; one message is shared by both fields.
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

/** A rule needed beforehand stays under the field; why the field exists goes behind a "?". */
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

  test("keeps the control's name the label, and describes it by the rule and then the explanation", () => {
    withHelp();

    // Beside the label, so the name is not "Attempts Hint".
    const control = screen.getByRole("textbox", { name: "Attempts" });
    expect(control).toHaveAccessibleName("Attempts");
    // The rule first, then the explanation.
    expect(control).toHaveAccessibleDescription(`${RULE} ${WHY}`);
  });

  test("still lets an error take the rule's slot, and keeps the caller's aria-invalid", () => {
    withHelp({ error: "Must be a whole number." });

    const control = screen.getByRole("textbox", { name: "Attempts" });
    expect(control).toHaveAttribute("aria-invalid", "true");
    // The error replaces the rule, not the explanation.
    expect(control).toHaveAccessibleDescription(`Must be a whole number. ${WHY}`);
    expect(screen.queryByText(RULE)).toBeNull();
  });

  /** Screen-reader users moving between fields land on the control, never on the "?". */
  test("describes the control by its explanation, so moving between fields still reads it", () => {
    render(
      <Field id="penalty" label="Penalty" help="Taken off for every wrong attempt already made." helpLabel="Explain">
        <input />
      </Field>,
    );

    const control = screen.getByLabelText("Penalty");
    expect(control).toHaveAccessibleDescription("Taken off for every wrong attempt already made.");
  });

  test("keeps the visible rule in the description alongside the explanation", () => {
    render(
      <Field
        id="network"
        label="Network"
        hint="CIDR ranges, comma separated."
        help="Checked on every attempt, against the address a trusted proxy reports."
        helpLabel="Explain"
      >
        <input />
      </Field>,
    );

    const described = screen.getByLabelText("Network").getAttribute("aria-describedby") ?? "";
    expect(described.split(" ")).toHaveLength(2);
    expect(screen.getByLabelText("Network")).toHaveAccessibleDescription(
      "CIDR ranges, comma separated. Checked on every attempt, against the address a trusted proxy reports.",
    );
  });
});
