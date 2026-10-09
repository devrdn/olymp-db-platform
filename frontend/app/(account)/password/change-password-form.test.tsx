import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Action would pull in `next/headers`.
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

  /** A mismatch blames only the two fields that disagree. */
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

  /** `wrong_password` blames only the current password. */
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

  /** Autocomplete hints keep the password manager from swapping old and new. */
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
