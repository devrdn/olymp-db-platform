import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The real Server Action would pull in `next/headers`.
const signInAction = vi.hoisted(() => vi.fn());
vi.mock("./actions", () => ({ signInAction }));

import { signInErrors } from "./codes";
import { SignInForm } from "./sign-in-form";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

/** What the page hands the form: its own texts and the sentences it can show. */
const slice = () => ({ signIn: en.auth.signIn, errors: signInErrors(en.errors) });

const NONE = { signUp: false, forgot: false, providers: [] };

describe("SignInForm, a rejected sign-in", () => {
  test("announces the failure in the interface's own words, not the server's", async () => {
    signInAction.mockResolvedValue({ code: "invalid_credentials" });
    render(<SignInForm dict={slice()} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.signIn.submit }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.invalid_credentials);
  });

  /**
   * `invalid_credentials` does not say which field, so both are invalid and
   * point at the one message.
   */
  test("marks both fields invalid and points them at the message", async () => {
    signInAction.mockResolvedValue({ code: "invalid_credentials" });
    render(<SignInForm dict={slice()} />);

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
    render(<SignInForm dict={slice()} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.signIn.submit }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(en.errors.sign_in_busy);
    expect(alert).not.toHaveTextContent(en.errors.invalid_credentials);
  });

  test("does not mark the fields invalid when the server was too busy to check them", async () => {
    // Nothing typed was wrong, so no field is invalid.
    signInAction.mockResolvedValue({ code: "sign_in_busy" });
    render(<SignInForm dict={slice()} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.signIn.submit }));
    await screen.findByRole("alert");

    for (const label of [en.auth.signIn.login, en.auth.signIn.password]) {
      expect(screen.getByLabelText(label)).not.toHaveAttribute("aria-invalid", "true");
    }
  });

  test("falls back to a general message for a code the dictionary does not know", async () => {
    signInAction.mockResolvedValue({ code: "meteor_strike" });
    render(<SignInForm dict={slice()} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.signIn.submit }));

    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.fallback);
  });
});

describe("SignInForm, before anything has been submitted", () => {
  test("marks neither field invalid", () => {
    render(<SignInForm dict={slice()} />);

    for (const label of [en.auth.signIn.login, en.auth.signIn.password]) {
      expect(screen.getByLabelText(label)).not.toHaveAttribute("aria-invalid");
    }
  });
});

describe("SignInForm, carrying the interrupted journey", () => {
  test("submits the path the guard captured", async () => {
    signInAction.mockResolvedValue({});
    render(<SignInForm dict={slice()} next="/contests?status=draft" />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.signIn.submit }));

    const [, form] = signInAction.mock.calls.at(-1) as [unknown, FormData];
    expect(form.get("next")).toBe("/contests?status=draft");
  });

  test("submits nothing extra when there was no interrupted journey", async () => {
    signInAction.mockResolvedValue({});
    render(<SignInForm dict={slice()} />);

    await userEvent.click(screen.getByRole("button", { name: en.auth.signIn.submit }));

    const [, form] = signInAction.mock.calls.at(-1) as [unknown, FormData];
    expect(form.get("next")).toBeNull();
  });
});

describe("SignInForm, the other ways in", () => {
  test("offers neither link unless told the installation has them", () => {
    render(<SignInForm dict={slice()} />);

    expect(screen.queryByRole("link", { name: en.auth.signIn.signUp })).not.toBeInTheDocument();
    expect(screen.queryByRole("link", { name: en.auth.signIn.forgot })).not.toBeInTheDocument();
  });

  test("offers each link it is told of", () => {
    render(<SignInForm dict={slice()} offers={{ ...NONE, signUp: true, forgot: true }} />);

    expect(screen.getByRole("link", { name: en.auth.signIn.signUp })).toHaveAttribute("href", "/signup");
    expect(screen.getByRole("link", { name: en.auth.signIn.forgot })).toHaveAttribute("href", "/forgot");
  });
});

describe("SignInForm, the providers under the password form", () => {
  const ALL = ["microsoft", "google", "github"] as const;
  const NAMES = { microsoft: "Microsoft", google: "Google", github: "GitHub" };
  const providerLinks = () => screen.queryAllByRole("link", { name: /^Continue with / });

  test("shows no divider and no provider link without providers", () => {
    render(<SignInForm dict={slice()} offers={{ ...NONE, signUp: true, forgot: true }} />);

    expect(providerLinks()).toHaveLength(0);
    expect(screen.queryByText(en.auth.signIn.or)).not.toBeInTheDocument();
  });

  test("shows an or divider and one link per provider, in the order given", () => {
    render(<SignInForm dict={slice()} offers={{ ...NONE, providers: [...ALL] }} />);

    expect(screen.getByText(en.auth.signIn.or)).toBeInTheDocument();
    const links = providerLinks();
    expect(links.map((link) => link.textContent)).toEqual([
      "Continue with Microsoft",
      "Continue with Google",
      "Continue with GitHub",
    ]);
    links.forEach((link, i) => {
      expect(link).toHaveAccessibleName(`Continue with ${NAMES[ALL[i]]}`);
      expect(link).not.toHaveAttribute("aria-label");
      expect(link.querySelector("svg")).not.toBeNull();
      expect(link).toHaveAttribute("href", `/api/v1/auth/${ALL[i]}/start`);
    });
  });

  test("returns to the page the guard interrupted", () => {
    render(<SignInForm dict={slice()} next="/contests?status=draft" offers={{ ...NONE, providers: [...ALL] }} />);

    for (const [i, link] of providerLinks().entries()) {
      expect(link).toHaveAttribute("href", `/api/v1/auth/${ALL[i]}/start?return=%2Fcontests%3Fstatus%3Ddraft`);
    }
  });

  test.each(["//evil.example", "https://evil.example/", "/\\evil.example", "/my contests"])(
    "never passes the hostile next %j on",
    (next) => {
      render(<SignInForm dict={slice()} next={next} offers={{ ...NONE, providers: ["google"] }} />);

      expect(providerLinks()[0]).toHaveAttribute("href", "/api/v1/auth/google/start");
    },
  );
});

describe("signInErrors", () => {
  test("carries the sentences a password sign-in can end in, and the fallback, not every sentence", () => {
    const picked = signInErrors(en.errors);

    expect(picked.invalid_credentials).toBe(en.errors.invalid_credentials);
    expect(picked.sign_in_busy).toBe(en.errors.sign_in_busy);
    expect(picked.account_blocked).toBe(en.errors.account_blocked);
    expect(picked.too_many_attempts).toBe(en.errors.too_many_attempts);
    expect(picked.fallback).toBe(en.errors.fallback);
    expect(Object.keys(picked).length).toBeLessThan(Object.keys(en.errors).length / 10);
  });
});
