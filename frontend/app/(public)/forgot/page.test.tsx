import { render, screen } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("./actions", () => ({ forgotPasswordAction: vi.fn() }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import ForgotPage from "./page";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  serverRequest.mockReset();
});

const offering = (reset: boolean) =>
  serverRequest.mockResolvedValue({
    password_signup: false,
    password_reset: reset,
    microsoft: false,
    trusted_email_domain: "",
    providers: [],
  });

describe("the forgotten-password page", () => {
  test("asks for a login or an email when the installation offers recovery", async () => {
    offering(true);
    render(await ForgotPage());

    expect(screen.getByLabelText(en.auth.forgot.login)).toBeInTheDocument();
  });

  /** Recovery never serves staff; saying so reveals nothing about any account. */
  test("says that an administrator resets staff accounts", async () => {
    offering(true);
    render(await ForgotPage());

    expect(screen.getByText(en.auth.forgot.staff)).toBeInTheDocument();
  });

  test("says recovery is not offered when it is off", async () => {
    offering(false);
    render(await ForgotPage());

    expect(screen.getByText(en.auth.forgot.unavailable)).toBeInTheDocument();
    expect(screen.queryByLabelText(en.auth.forgot.login)).not.toBeInTheDocument();
  });
});
