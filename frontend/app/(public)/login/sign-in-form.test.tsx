import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Action would pull in `next/headers`.
const signInAction = vi.hoisted(() => vi.fn());
vi.mock("./actions", () => ({ signInAction }));

import { SignInForm } from "./sign-in-form";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

describe("SignInForm, a rejected sign-in", () => {
  test("announces the failure in the interface's own words, not the server's", async () => {
    signInAction.mockResolvedValue({ code: "invalid_credentials" });
    render(<SignInForm dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.signIn.submit }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.invalid_credentials);
  });

  /**
   * `invalid_credentials` does not say which field, so both are invalid and
   * point at the one message.
   */
  test("marks both fields invalid and points them at the message", async () => {
    signInAction.mockResolvedValue({ code: "invalid_credentials" });
    render(<SignInForm dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.signIn.submit }));
    await screen.findByRole("alert");

    for (const label of [en.auth.signIn.login, en.auth.signIn.password]) {
      const control = screen.getByLabelText(label);
      expect(control).toHaveAttribute("aria-invalid", "true");
      expect(control).toHaveAccessibleDescription(en.errors.invalid_credentials);
    }
  });

  /** A busy server never checked the password, so it must not say "wrong login or password". */
  test("tells the visitor to retry when the server is too busy to check", async () => {
    signInAction.mockResolvedValue({ code: "sign_in_busy" });
    render(<SignInForm dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.signIn.submit }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.errors.sign_in_busy);
    expect(alert).not.toHaveTextContent(en.errors.invalid_credentials);
  });

  test("does not mark the fields invalid when the server was too busy to check them", async () => {
    // Nothing typed was wrong, so no field is invalid.
    signInAction.mockResolvedValue({ code: "sign_in_busy" });
    render(<SignInForm dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.signIn.submit }));
    await screen.findByRole("alert");

    for (const label of [en.auth.signIn.login, en.auth.signIn.password]) {
      expect(screen.getByLabelText(label)).not.toHaveAttribute("aria-invalid", "true");
    }
  });

  test("falls back to a general message for a code the dictionary does not know", async () => {
    signInAction.mockResolvedValue({ code: "meteor_strike" });
    render(<SignInForm dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.signIn.submit }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.fallback);
  });
});

describe("SignInForm, before anything has been submitted", () => {
  test("marks neither field invalid", () => {
    render(<SignInForm dict={en} />);

    for (const label of [en.auth.signIn.login, en.auth.signIn.password]) {
      expect(screen.getByLabelText(label)).not.toHaveAttribute("aria-invalid");
    }
  });
});

describe("SignInForm, carrying the interrupted journey", () => {
  test("submits the path the guard captured", async () => {
    signInAction.mockResolvedValue({});
    render(<SignInForm dict={en} next="/contests?status=draft" />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.signIn.submit }));

    const [, form] = signInAction.mock.calls.at(-1) as [unknown, FormData];
    expect(form.get("next")).toBe("/contests?status=draft");
  });

  test("submits nothing extra when there was no interrupted journey", async () => {
    signInAction.mockResolvedValue({});
    render(<SignInForm dict={en} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.signIn.submit }));

    const [, form] = signInAction.mock.calls.at(-1) as [unknown, FormData];
    expect(form.get("next")).toBeNull();
  });
});
