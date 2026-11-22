import { render, screen } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

const { fetchIdentity, notFound, redirect } = vi.hoisted(() => ({
  fetchIdentity: vi.fn(),
  notFound: vi.fn(() => {
    throw new Error("NEXT_NOT_FOUND");
  }),
  redirect: vi.fn(() => {
    throw new Error("NEXT_REDIRECT");
  }),
}));
vi.mock("@/lib/auth/session", () => ({ fetchIdentity }));
vi.mock("next/navigation", () => ({ notFound, redirect }));
vi.mock("./actions", () => ({ createContestAction: vi.fn(async () => ({})) }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import NewContestPage from "./page";

let en: Dictionary;
beforeAll(async () => {
  en = await getDictionary("en");
});

beforeEach(() => vi.clearAllMocks());

function identityHolding(permissions: string[]) {
  return { id: "u", login: "u", fullName: "U", roles: [], permissions, managesContests: true };
}

describe("the new-contest page", () => {
  test("shows the form to a holder of contest.create", async () => {
    fetchIdentity.mockResolvedValue(identityHolding(["contest.create"]));

    render(await NewContestPage());

    expect(screen.getByRole("heading", { name: en.workspace.create.heading })).toBeInTheDocument();
    expect(notFound).not.toHaveBeenCalled();
  });

  test("is answered as not found for an account without contest.create", async () => {
    fetchIdentity.mockResolvedValue(identityHolding(["contest.read"]));

    await expect(NewContestPage()).rejects.toThrow("NEXT_NOT_FOUND");
    expect(notFound).toHaveBeenCalledOnce();
  });

  test("sends a dead session back to sign-in, then here", async () => {
    fetchIdentity.mockResolvedValue(null);

    await expect(NewContestPage()).rejects.toThrow("NEXT_REDIRECT");
    expect(redirect).toHaveBeenCalledWith("/login?next=%2Fcontests%2Fnew");
  });

  test("does not take an unreachable API for a missing permission", async () => {
    fetchIdentity.mockRejectedValue(new Error("unreachable"));

    await expect(NewContestPage()).rejects.toThrow("unreachable");
    expect(notFound).not.toHaveBeenCalled();
  });
});
