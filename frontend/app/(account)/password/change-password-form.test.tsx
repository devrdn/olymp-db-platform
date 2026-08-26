import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// A Server Action: importing it for real pulls in `next/headers` and a running
// framework. The module boundary is what gets faked; the form's own behaviour
// is what is under test.
const changePasswordAction = vi.hoisted(() => vi.fn());
vi.mock("./actions", () => ({ changePasswordAction }));

import { ChangePasswordForm } from "./change-password-form";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

describe("ChangePasswordForm, a refused change", () => {
  test("says the two new passwords differ, in the interface's own words", async () => {
    changePasswordAction.mockResolvedValue({ code: "password_mismatch" });
    render(<ChangePasswordForm dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.changePassword.submit }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.password_mismatch);
  });

  /**
   * A mismatch is the author's typing, and it belongs to the two fields that
   * disagree. The one-time password is not in question and marking it invalid
   * would send them to re-check a field that was right.
   */
  test("blames only the two fields a mismatch is about", async () => {
    changePasswordAction.mockResolvedValue({ code: "password_mismatch" });
    render(<ChangePasswordForm dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.changePassword.submit }));
    await screen.findByRole("alert");

    for (const label of [en.auth.changePassword.next, en.auth.changePassword.confirm]) {
      expect(screen.getByLabelText(label)).toHaveAttribute("aria-invalid", "true");
    }
    expect(screen.getByLabelText(en.auth.changePassword.current)).not.toHaveAttribute(
      "aria-invalid",
    );
  });

  /**
   * The mirror image: `wrong_password` is the API saying the handover secret
   * itself is wrong. Blaming the new password there would be worse than saying
   * nothing, because the author would change a field that was fine.
   */
  test("blames only the current password when the API rejects that one", async () => {
    changePasswordAction.mockResolvedValue({ code: "wrong_password" });
    render(<ChangePasswordForm dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.changePassword.submit }));
    await screen.findByRole("alert");

    expect(screen.getByLabelText(en.auth.changePassword.current)).toHaveAttribute(
      "aria-invalid",
      "true",
    );
    expect(screen.getByLabelText(en.auth.changePassword.next)).not.toHaveAttribute("aria-invalid");
  });

  test("points every blamed field at the one message", async () => {
    changePasswordAction.mockResolvedValue({ code: "weak_password" });
    render(<ChangePasswordForm dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.changePassword.submit }));
    await screen.findByRole("alert");

    expect(screen.getByLabelText(en.auth.changePassword.next)).toHaveAccessibleDescription(
      en.errors.weak_password,
    );
  });

  test("falls back to a general message for a code the dictionary does not know", async () => {
    changePasswordAction.mockResolvedValue({ code: "meteor_strike" });
    render(<ChangePasswordForm dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.changePassword.submit }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.fallback);
  });
});

describe("ChangePasswordForm, before anything has been submitted", () => {
  test("marks no field invalid", () => {
    render(<ChangePasswordForm dict={en} />);

    for (const label of [
      en.auth.changePassword.current,
      en.auth.changePassword.next,
      en.auth.changePassword.confirm,
    ]) {
      expect(screen.getByLabelText(label)).not.toHaveAttribute("aria-invalid");
    }
  });

  /**
   * The browser's password manager needs to be told which field is which, or
   * it offers the saved password for the new one and stores the old.
   */
  test("tells the password manager which field is the new one", () => {
    render(<ChangePasswordForm dict={en} />);

    expect(screen.getByLabelText(en.auth.changePassword.current)).toHaveAttribute(
      "autocomplete",
      "current-password",
    );
    for (const label of [en.auth.changePassword.next, en.auth.changePassword.confirm]) {
      expect(screen.getByLabelText(label)).toHaveAttribute("autocomplete", "new-password");
    }
  });
});
