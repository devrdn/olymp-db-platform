import { render, screen, within } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { serverRequest, redirect } = vi.hoisted(() => ({
  serverRequest: vi.fn(),
  redirect: vi.fn((target: string) => {
    throw new Error(`NEXT_REDIRECT ${target}`);
  }),
}));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("next/navigation", () => ({ redirect }));
vi.mock("./actions", () => ({ switchProviderAction: vi.fn() }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import IntegrationsPage from "./page";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  serverRequest.mockReset();
  redirect.mockClear();
  vi.spyOn(console, "warn").mockImplementation(() => {});
});

const URI = "https://contest.example.edu/api/v1/auth";

function answer(overrides: Record<string, object> = {}) {
  const base = {
    microsoft: {
      id: "microsoft",
      configured: true,
      enabled: true,
      redirect_uri: `${URI}/microsoft/callback`,
      variables: ["MICROSOFT_CLIENT_ID", "MICROSOFT_CLIENT_SECRET"],
    },
    google: {
      id: "google",
      configured: true,
      enabled: false,
      redirect_uri: `${URI}/google/callback`,
      variables: ["GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET"],
    },
    github: {
      id: "github",
      configured: false,
      enabled: true,
      redirect_uri: `${URI}/github/callback`,
      variables: ["GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET"],
    },
  };
  serverRequest.mockResolvedValue({ providers: Object.values({ ...base, ...overrides }) });
}

async function renderPage() {
  render(await IntegrationsPage());
}

const section = (name: string) => screen.getByRole("region", { name });
const t = () => en.settings.integrations;

describe("the integrations page", () => {
  test("lists the three providers in order, under a heading and lede", async () => {
    answer();
    await renderPage();

    expect(screen.getByRole("heading", { level: 1, name: t().heading })).toBeVisible();
    expect(screen.getByText(t().lede)).toBeVisible();
    expect(screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent)).toEqual([
      "Microsoft",
      "Google",
      "GitHub",
    ]);
    expect(serverRequest).toHaveBeenCalledWith("/integrations");
  });

  test("an unconfigured provider names the variables to set and has no switch", async () => {
    answer();
    await renderPage();

    const github = within(section("GitHub"));
    expect(github.getByText(t().notConfigured)).toBeVisible();
    expect(github.getByText(t().toConfigure)).toBeVisible();
    expect(github.getByText("GITHUB_CLIENT_ID")).toBeVisible();
    expect(github.getByText("GITHUB_CLIENT_SECRET")).toBeVisible();
    expect(github.queryByRole("button", { name: t().switchOn })).toBeNull();
    expect(github.queryByRole("button", { name: t().switchOff })).toBeNull();
  });

  test("an enabled provider is on, shows its redirect URI, and offers to switch off beside the note", async () => {
    answer();
    await renderPage();

    const microsoft = within(section("Microsoft"));
    expect(microsoft.getByText(t().on)).toBeVisible();
    expect(microsoft.getByText(`${URI}/microsoft/callback`)).toBeVisible();
    expect(microsoft.getByRole("button", { name: t().copy })).toBeVisible();
    expect(microsoft.getByRole("button", { name: t().switchOff })).toBeVisible();
    expect(microsoft.getByText(t().note)).toBeVisible();
  });

  test("a disabled provider is off and offers to switch on", async () => {
    answer();
    await renderPage();

    const google = within(section("Google"));
    expect(google.getByText(t().off)).toBeVisible();
    expect(google.getByRole("button", { name: t().switchOn })).toBeVisible();
  });

  test("without a public address it says so instead of a redirect URI", async () => {
    answer({ google: { id: "google", configured: true, enabled: true, redirect_uri: "", variables: [] } });
    await renderPage();

    const google = within(section("Google"));
    expect(google.getByText(t().noPublicUrl)).toBeVisible();
    expect(google.queryByRole("button", { name: t().copy })).toBeNull();
  });

  test("an unauthenticated caller is sent to sign in, as the settings page does", async () => {
    serverRequest.mockRejectedValue(new ApiError("unauthenticated", 401, "no"));

    await expect(renderPage()).rejects.toThrow("NEXT_REDIRECT /login?next=%2Fintegrations");
  });
});
