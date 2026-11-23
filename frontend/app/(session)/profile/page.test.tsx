import { render, screen } from "@testing-library/react";
import { afterEach, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// A Server Component: the API, the session and the request are faked.
const { serverRequest, branding } = vi.hoisted(() => ({
  serverRequest: vi.fn(),
  branding: vi.fn(),
}));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("@/lib/api/branding", () => ({ branding }));
vi.mock("@/lib/auth/session", () => ({
  fetchIdentity: async () => ({
    id: "9a1f0c3e-2b44-4e77-8d0a-1c5b8e91a4d6",
    login: "s.popescu",
    fullName: "Sergiu Popescu",
    roles: ["student"],
    permissions: [],
    managesContests: false,
  }),
}));
vi.mock("@/components/layout/session-actions", () => ({ signOutAction: vi.fn() }));
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  redirect: vi.fn(),
  usePathname: () => "/profile",
  useRouter: () => ({ refresh: vi.fn(), push: vi.fn() }),
}));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import ProfilePage from "./page";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

beforeEach(() => {
  vi.clearAllMocks();
  serverRequest.mockImplementation(async (path: string) => {
    if (path === "/me/summary") return { contests: 0, finished: 0, queries: 0, solved: 0 };
    if (path === "/me/contests") return { items: [], truncated: false };
    throw new Error(`unexpected ${path}`);
  });
});

function brandingWith(feedback: string) {
  branding.mockResolvedValue({ name: "USM", contact: "", feedback, images: {} });
}

const feedbackLink = () => screen.queryByRole("link", { name: new RegExp(dict.participant.feedback.link) });

describe("the profile's feedback link", () => {
  test("is offered when the installation has a feedback address", async () => {
    brandingWith("https://forms.gle/beta");

    render(await ProfilePage());

    const link = feedbackLink();
    expect(link).toHaveAttribute("href", "https://forms.gle/beta");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });

  test("is absent when the installation has none", async () => {
    brandingWith("");

    render(await ProfilePage());

    expect(screen.getByRole("link", { name: dict.profile.changePassword })).toBeInTheDocument();
    expect(feedbackLink()).not.toBeInTheDocument();
  });
});

describe("a profile section that could not be read", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  test("costs the page nothing, and is logged on one line without what the failure said", async () => {
    brandingWith("");
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    serverRequest.mockImplementation(async (path: string) => {
      if (path === "/me/summary") {
        throw new TypeError('Headers.append: "dbcontest_session=secret-token\nFAKE LINE" is an invalid header value.');
      }
      return { items: [], truncated: false };
    });

    render(await ProfilePage());

    expect(screen.getByRole("heading", { level: 1, name: "Sergiu Popescu" })).toBeInTheDocument();
    expect(logged).toHaveBeenCalledTimes(1);
    const line = logged.mock.calls[0].map(String).join(" ");
    expect(line).toContain("/me/summary");
    expect(line).not.toContain("secret-token");
    expect(line).not.toMatch(/[\r\n]/);
  });

  test("writes no log line for a reader past their own profile budget", async () => {
    brandingWith("");
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    serverRequest.mockRejectedValue(new ApiError("profile_too_often", 429, "Too often"));

    render(await ProfilePage());

    expect(screen.getByRole("heading", { level: 1, name: "Sergiu Popescu" })).toBeInTheDocument();
    expect(logged).not.toHaveBeenCalled();
  });
});
