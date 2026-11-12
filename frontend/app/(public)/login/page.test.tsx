import { render, screen } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// A Server Component: the API and the request are faked.
const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("./actions", () => ({ signInAction: vi.fn() }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import LoginPage from "./page";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  serverRequest.mockReset();
  vi.spyOn(console, "warn").mockImplementation(() => {});
});

function methods(signup: boolean, reset: boolean) {
  serverRequest.mockImplementation(async (path: string) => {
    if (path === "/auth/methods") {
      return { password_signup: signup, password_reset: reset, microsoft: false, trusted_email_domain: "" };
    }
    throw new Error(`unexpected ${path}`);
  });
}

async function renderPage() {
  render(await LoginPage({ params: Promise.resolve({}), searchParams: Promise.resolve({}) } as PageProps<"/login">));
}

const signUpLink = () => screen.queryByRole("link", { name: en.auth.signIn.signUp });
const forgotLink = () => screen.queryByRole("link", { name: en.auth.signIn.forgot });

describe("the sign-in page's other ways in", () => {
  test("offers an account and a forgotten password when the installation does", async () => {
    methods(true, true);
    await renderPage();

    expect(signUpLink()).toHaveAttribute("href", "/signup");
    expect(forgotLink()).toHaveAttribute("href", "/forgot");
  });

  test("hides creating an account when sign-up is off", async () => {
    methods(false, true);
    await renderPage();

    expect(signUpLink()).not.toBeInTheDocument();
    expect(forgotLink()).toBeInTheDocument();
  });

  test("hides the forgotten password when recovery is off", async () => {
    methods(true, false);
    await renderPage();

    expect(signUpLink()).toBeInTheDocument();
    expect(forgotLink()).not.toBeInTheDocument();
  });

  /** Signing in must not depend on the API saying what else it offers. */
  test("hides both, and still signs in, when the API cannot say", async () => {
    serverRequest.mockRejectedValue(new ApiError("unreachable", 502, "down"));
    await renderPage();

    expect(signUpLink()).not.toBeInTheDocument();
    expect(forgotLink()).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: en.auth.signIn.submit })).toBeInTheDocument();
  });
});
