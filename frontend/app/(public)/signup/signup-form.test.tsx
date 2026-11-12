import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Action would pull in `next/headers`.
const signUpAction = vi.hoisted(() => vi.fn());
vi.mock("./actions", () => ({ signUpAction }));

import { SignUpForm } from "./signup-form";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  signUpAction.mockReset();
});

const submit = () => userEvent.click(screen.getByRole("button", { name: en.auth.signUp.submit }));

function renderForm() {
  return render(<SignUpForm t={en.auth.signUp} errors={en.errors} />);
}

describe("SignUpForm, before anything has been submitted", () => {
  test("labels every field from the dictionary", () => {
    renderForm();

    for (const label of [
      en.auth.signUp.fullName,
      en.auth.signUp.login,
      en.auth.signUp.email,
      en.auth.signUp.password,
      en.auth.signUp.confirm,
    ]) {
      expect(screen.getByLabelText(label)).not.toHaveAttribute("aria-invalid");
    }
  });

  /** The rules are said before anyone breaks them. */
  test("states the login, full-name and password rules up front", () => {
    renderForm();

    expect(screen.getByLabelText(en.auth.signUp.login)).toHaveAccessibleDescription(en.auth.signUp.loginHint);
    expect(screen.getByLabelText(en.auth.signUp.fullName)).toHaveAccessibleDescription(
      en.auth.signUp.fullNameHint,
    );
    expect(screen.getByLabelText(en.auth.signUp.password)).toHaveAccessibleDescription(
      en.auth.signUp.passwordHint,
    );
  });

  /** `users.MinPasswordLength`: the hint gives the real minimum. */
  test("gives the password minimum the API applies", () => {
    expect(en.auth.signUp.passwordHint).toContain("12");
  });

  test("lowers the login's case as it is typed", async () => {
    renderForm();

    await userEvent.type(screen.getByLabelText(en.auth.signUp.login), "Ana_P");

    expect(screen.getByLabelText(en.auth.signUp.login)).toHaveValue("ana_p");
  });

  test("tells the password manager both password fields are a new password", () => {
    renderForm();

    for (const label of [en.auth.signUp.password, en.auth.signUp.confirm]) {
      expect(screen.getByLabelText(label)).toHaveAttribute("autocomplete", "new-password");
    }
  });

  test("offers sign-in to somebody who already has an account", () => {
    renderForm();

    expect(screen.getByRole("link", { name: en.auth.signUp.signIn })).toHaveAttribute("href", "/login");
  });
});

describe("SignUpForm, a refused sign-up", () => {
  test.each([
    "login_taken",
    "weak_password",
    "password_mismatch",
    "invalid_request",
    "signup_not_available",
    "signup_full",
    "too_many_attempts",
    "sign_in_busy",
    "unreachable",
  ] as const)("says what %s means in the interface's own words", async (code) => {
    signUpAction.mockResolvedValue({ code });
    renderForm();

    await submit();

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors[code]);
  });

  test("falls back to a general message for a code the dictionary does not know", async () => {
    signUpAction.mockResolvedValue({ code: "meteor_strike" });
    renderForm();

    await submit();

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.fallback);
  });

  test.each([
    ["login_taken", ["login"]],
    ["weak_password", ["password"]],
    ["password_mismatch", ["password", "confirm"]],
  ] as const)("blames only the fields %s is about", async (code, blamed) => {
    signUpAction.mockResolvedValue({ code });
    renderForm();

    await submit();
    await screen.findByRole("alert");

    for (const field of ["fullName", "login", "email", "password", "confirm"] as const) {
      const control = screen.getByLabelText(en.auth.signUp[field]);
      if ((blamed as readonly string[]).includes(field)) {
        expect(control).toHaveAttribute("aria-invalid", "true");
        expect(control).toHaveAccessibleDescription(expect.stringContaining(en.errors[code]));
      } else {
        expect(control).not.toHaveAttribute("aria-invalid");
      }
    }
  });

  /** The form's own check knows which field broke which rule. */
  test.each([
    ["login", "login", "loginHint"],
    ["fullName", "fullName", "fullNameHint"],
    ["fullNameLength", "fullName", "fullNameTooLong"],
    ["passwordLength", "password", "passwordHint"],
  ] as const)("says what %s broke, under its field and nowhere else", async (broken, field, text) => {
    signUpAction.mockResolvedValue({ code: "invalid_request", broken, values: { login: "a.b" } });
    renderForm();

    await submit();

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.auth.signUp[text]);
    expect(screen.getByLabelText(en.auth.signUp[field])).toHaveAttribute("aria-invalid", "true");
    expect(screen.getByLabelText(en.auth.signUp[field])).toHaveAccessibleDescription(en.auth.signUp[text]);
    expect(screen.getAllByRole("alert")).toHaveLength(1);
  });

  test("keeps what was typed, but not the passwords", async () => {
    signUpAction.mockResolvedValue({
      code: "login_taken",
      values: { fullName: "Ana Popescu", login: "ana", email: "ana@example.com" },
    });
    renderForm();

    await submit();
    await screen.findByRole("alert");

    expect(screen.getByLabelText(en.auth.signUp.fullName)).toHaveValue("Ana Popescu");
    expect(screen.getByLabelText(en.auth.signUp.login)).toHaveValue("ana");
    expect(screen.getByLabelText(en.auth.signUp.email)).toHaveValue("ana@example.com");
    expect(screen.getByLabelText(en.auth.signUp.password)).toHaveValue("");
  });
});
