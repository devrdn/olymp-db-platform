import { render, screen } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// A Server Component: the API, the session and the request are faked.
const { serverRequest, fetchIdentity } = vi.hoisted(() => ({
  serverRequest: vi.fn(),
  fetchIdentity: vi.fn(),
}));
vi.mock("@/lib/api/server", () => ({ serverRequest }));
vi.mock("@/lib/auth/session", () => ({ fetchIdentity }));
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  redirect: vi.fn(),
}));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import ContestsPage from "./page";

let en: Dictionary;
beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => {
  vi.clearAllMocks();
  serverRequest.mockResolvedValue({ items: [], total: 0 });
});

const renderPage = async () =>
  render(await ContestsPage({ searchParams: Promise.resolve({}) } as never));

const newContest = () => screen.queryByRole("link", { name: en.workspace.create.action });

function identityHolding(permissions: string[]) {
  return { id: "u", login: "u", fullName: "U", roles: [], permissions, managesContests: true };
}

describe("the contest register's new-contest link", () => {
  test("is offered to a holder of contest.create", async () => {
    fetchIdentity.mockResolvedValue(identityHolding(["contest.create"]));
    await renderPage();

    expect(newContest()).toBeInTheDocument();
  });

  test("is withheld from a manager who may not create contests", async () => {
    fetchIdentity.mockResolvedValue(identityHolding([]));
    await renderPage();

    expect(newContest()).toBeNull();
  });

  test("is withheld when the API cannot say who the caller is", async () => {
    fetchIdentity.mockRejectedValue(new Error("unreachable"));
    await renderPage();

    expect(newContest()).toBeNull();
  });
});
