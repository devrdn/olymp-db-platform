import { render, screen } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { IdentityUnavailableError, type CurrentIdentity } from "@/lib/auth/session";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// A Server Component: the API and the request are faked.
const { serverRequest, fetchIdentity, redirect } = vi.hoisted(() => ({
  serverRequest: vi.fn(),
  fetchIdentity: vi.fn(),
  redirect: vi.fn((target: string) => {
    throw new Error(`NEXT_REDIRECT ${target}`);
  }),
}));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("@/lib/auth/session", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/auth/session")>()),
  fetchIdentity,
}));
vi.mock("next/navigation", () => ({ redirect }));
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
  redirect.mockClear();
  fetchIdentity.mockReset();
  fetchIdentity.mockResolvedValue(null);
  vi.spyOn(console, "warn").mockImplementation(() => {});
});

function methods(signup: boolean, reset: boolean, providers: string[] = []) {
  serverRequest.mockImplementation(async (path: string) => {
    if (path === "/auth/methods") {
      return { password_signup: signup, password_reset: reset, microsoft: providers.includes("microsoft"), trusted_email_domain: "", providers: providers.map((id) => ({ id })) };
    }
    throw new Error(`unexpected ${path}`);
  });
}

async function renderPage(search: Record<string, string | string[]> = {}) {
  render(await LoginPage({ params: Promise.resolve({}), searchParams: Promise.resolve(search) } as PageProps<"/login">));
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

describe("the sign-in page's providers", () => {
  const providerLinks = () => screen.queryAllByRole("link", { name: /^Continue with / });

  test("offers each one the installation has, returning to the interrupted page", async () => {
    methods(false, false, ["google"]);
    await renderPage({ next: "/contests/abc/play" });

    const links = providerLinks();
    expect(links).toHaveLength(1);
    expect(links[0]).toHaveTextContent("Continue with Google");
    expect(links[0]).toHaveAttribute("href", "/api/v1/auth/google/start?return=%2Fcontests%2Fabc%2Fplay");
  });

  test("offers none otherwise", async () => {
    methods(true, true);
    await renderPage();

    expect(providerLinks()).toHaveLength(0);
  });

  test("offers none when the API cannot say", async () => {
    serverRequest.mockRejectedValue(new ApiError("unreachable", 502, "down"));
    await renderPage();

    expect(providerLinks()).toHaveLength(0);
  });
});

describe("the sign-in page after an external sign-in came back refused", () => {
  test.each([
    "external_signin_failed",
    "external_signin_staff_account",
    "external_signin_closed",
    "external_signin_conflict",
    "external_signin_unavailable",
    "account_blocked",
  ] as const)("says what %s means", async (code) => {
    methods(false, false, ["microsoft"]);
    await renderPage({ error: code });

    expect(screen.getByRole("alert")).toHaveTextContent(en.errors[code]);
  });

  /** The address may select a sentence, never supply text. */
  test.each(["invalid_credentials", "<b>Your account is suspended</b>", "fallback", "constructor"])(
    "shows nothing for %j, and never the value itself",
    async (value) => {
      methods(false, false, ["microsoft"]);
      await renderPage({ error: value });

      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expect(document.body).not.toHaveTextContent(value);
    },
  );

  test("shows nothing for a repeated parameter", async () => {
    methods(false, false, ["microsoft"]);
    await renderPage({ error: ["external_signin_failed", "external_signin_closed"] });

    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });
});

/**
 * A staff account made Microsoft-only before its promotion has no password;
 * the refusal must give it a way forward.
 */
describe.each([
  ["en", /ask an administrator to reset its password/],
  ["ro", /rugați un administrator să îi reseteze parola/],
  ["ru", /попросите администратора сбросить её пароль/],
] as const)("the staff refusal in %s", (locale, administrator) => {
  test("offers an administrator's reset beside the password", async () => {
    const sentence = (await getDictionary(locale)).errors.external_signin_staff_account;

    expect(sentence).toMatch(administrator);
  });
});

describe.each(["en", "ro", "ru"] as const)("the switched-off refusal in %s", (locale) => {
  // Recovery does not exist with mail off and refuses staff, so the sentence
  // must not send everyone to it.
  test("does not promise password recovery", async () => {
    const dict = await getDictionary(locale);

    expect(dict.errors.external_signin_unavailable).not.toContain(dict.auth.signIn.forgot);
  });
});

describe.each(["en", "ro", "ru"] as const)("the sign-in lede in %s", (locale) => {
  // Self sign-up and Microsoft both exist; the lede must not say every account
  // is handed out by an organisation.
  test("makes no claim about who hands out the login", async () => {
    const lede = (await getDictionary(locale)).auth.signIn.lede;

    expect(lede).not.toMatch(/department|catedr|кафедр/i);
  });
});

/**
 * A Microsoft sign-in without a return path ends here, signed in. The page
 * sends the visitor on as a password sign-in would, so where an account lands
 * is decided by one rule.
 */
describe("the sign-in page for a visitor already signed in", () => {
  function signedIn(permissions: string[]): CurrentIdentity {
    return { id: "u-1", login: "ana", fullName: "Ana", roles: [], permissions };
  }

  test("sends a participant to their contests", async () => {
    methods(false, false, ["microsoft"]);
    fetchIdentity.mockResolvedValue(signedIn([]));

    await expect(renderPage()).rejects.toThrow("NEXT_REDIRECT /my");
  });

  test("sends staff to the constructor", async () => {
    methods(false, false, ["microsoft"]);
    fetchIdentity.mockResolvedValue(signedIn(["contest.create"]));

    await expect(renderPage()).rejects.toThrow("NEXT_REDIRECT /contests");
  });

  test("resumes the interrupted page", async () => {
    methods(false, false, ["microsoft"]);
    fetchIdentity.mockResolvedValue(signedIn([]));

    await expect(renderPage({ next: "/contests/abc/play" })).rejects.toThrow("NEXT_REDIRECT /contests/abc/play");
  });

  /** A refused external sign-in may come back to a browser still signed in. */
  test("stays to say why an external sign-in was refused", async () => {
    methods(false, false, ["microsoft"]);
    fetchIdentity.mockResolvedValue(signedIn([]));
    await renderPage({ error: "external_signin_failed" });

    expect(redirect).not.toHaveBeenCalled();
    expect(screen.getByRole("alert")).toHaveTextContent(en.errors.external_signin_failed);
  });

  test("shows the form when the API cannot say who the visitor is", async () => {
    methods(false, false, ["microsoft"]);
    fetchIdentity.mockRejectedValue(new IdentityUnavailableError(502));
    await renderPage();

    expect(redirect).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: en.auth.signIn.submit })).toBeInTheDocument();
  });
});
