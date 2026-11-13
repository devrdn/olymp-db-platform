import { render, screen } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("./actions", () => ({ signUpAction: vi.fn() }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import SignUpPage from "./page";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  serverRequest.mockReset();
  vi.spyOn(console, "warn").mockImplementation(() => {});
});

const offering = (signup: boolean) =>
  serverRequest.mockResolvedValue({
    password_signup: signup,
    password_reset: true,
    microsoft: false,
    trusted_email_domain: "",
    providers: [],
  });

describe("the sign-up page", () => {
  test("shows the form when the installation offers sign-up", async () => {
    offering(true);
    render(await SignUpPage());

    expect(screen.getByRole("heading", { level: 1, name: en.auth.signUp.title })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: en.auth.signUp.submit })).toBeInTheDocument();
  });

  test("says sign-up is not offered, and offers sign-in, when it is off", async () => {
    offering(false);
    render(await SignUpPage());

    expect(screen.getByText(en.errors.signup_not_available)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: en.auth.signUp.submit })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.auth.signUp.signIn })).toHaveAttribute("href", "/login");
  });

  /** The API stays the authority: when it cannot say, the form asks it. */
  test("shows the form when the API cannot say what it offers", async () => {
    serverRequest.mockRejectedValue(new ApiError("unreachable", 502, "down"));
    render(await SignUpPage());

    expect(screen.getByRole("button", { name: en.auth.signUp.submit })).toBeInTheDocument();
  });
});
