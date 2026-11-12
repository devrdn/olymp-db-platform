import { render, screen } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { jar } = vi.hoisted(() => ({ jar: { get: vi.fn() } }));
vi.mock("next/headers", () => ({ cookies: async () => jar }));
vi.mock("./actions", () => ({ confirmSignupAction: vi.fn(), resendCodeAction: vi.fn() }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import ConfirmPage from "./page";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  vi.stubEnv("COOKIE_SECURE", "false");
  jar.get.mockReset();
});

function browserHoldsAddress(email: string | undefined) {
  jar.get.mockImplementation((name: string) =>
    name === "dbcontest_signup_email" && email ? { name, value: email } : undefined,
  );
}

describe("the confirmation page", () => {
  /** It names the inbox to look in, and never whether an account holds it. */
  test("names the address the code went to, read from its cookie", async () => {
    browserHoldsAddress("ana@example.com");
    render(await ConfirmPage());

    expect(screen.getByText(en.auth.confirm.lede.replace("{email}", "ana@example.com"))).toBeInTheDocument();
    expect(screen.getByLabelText(en.auth.confirm.code)).toBeInTheDocument();
  });

  test("offers to start again, and asks for no code, when the browser holds no address", async () => {
    browserHoldsAddress(undefined);
    render(await ConfirmPage());

    expect(screen.getByText(en.auth.confirm.nothingToConfirm)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.auth.confirm.startOver })).toHaveAttribute("href", "/signup");
    expect(screen.queryByLabelText(en.auth.confirm.code)).not.toBeInTheDocument();
  });
});
