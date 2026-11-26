import { render, screen } from "@testing-library/react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import { ApiError } from "@/lib/api/client";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// A Server Component: the API, the session and the request are faked.
const { serverRequest } = vi.hoisted(() => ({ serverRequest: vi.fn() }));
vi.mock("next/cache", () => ({ revalidatePath: vi.fn() }));
vi.mock("@/lib/api/server", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/api/server")>()),
  serverRequest,
}));
vi.mock("@/lib/auth/session", () => ({
  fetchIdentity: async () => ({
    id: "a0000000-0000-4000-8000-000000000001",
    login: "admin",
    fullName: "Administrator",
    roles: ["admin"],
    permissions: ["users.manage"],
    managesContests: false,
  }),
}));
vi.mock("../delivery", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../delivery")>()),
  readMailOffer: async () => ({ status: "off" }),
}));
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  useRouter: () => ({ refresh: vi.fn(), push: vi.fn() }),
}));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});

import AccountPage from "./page";

const userId = "9a1f0c3e-2b44-4e77-8d0a-1c5b8e91a4d6";

let en: Dictionary;
beforeAll(async () => {
  en = await getDictionary("en");
});

/** The API with the identities read answered by `identities`. */
function answering(identities: () => Promise<unknown>) {
  serverRequest.mockImplementation(async (path: string) => {
    if (path === `/users/${userId}`) {
      return {
        id: userId,
        login: "s.popescu",
        full_name: "Sergiu Popescu",
        status: "active",
        roles: ["student"],
        created_at: "2026-02-01T10:00:00Z",
      };
    }
    if (path === "/roles") return { items: [{ code: "student", name: "Student" }] };
    if (path === `/users/${userId}/identities`) return identities();
    throw new Error(`unexpected ${path}`);
  });
}

const renderPage = async () =>
  render(await AccountPage({ params: Promise.resolve({ userId }) } as PageProps<"/users/[userId]">));

beforeEach(() => {
  serverRequest.mockReset();
});

describe("the account page", () => {
  test("lists the account's sign-in services", async () => {
    answering(async () => ({ has_password: true, items: [] }));

    await renderPage();

    expect(screen.getByRole("region", { name: en.accounts.card.identities })).toBeInTheDocument();
  });

  test("still shows the account when its sign-in services could not be read", async () => {
    const quiet = vi.spyOn(console, "error").mockImplementation(() => {});
    answering(async () => {
      throw new ApiError("internal_error", 500, "boom");
    });

    await renderPage();

    expect(screen.getByRole("heading", { level: 1, name: "Sergiu Popescu" })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: en.accounts.card.danger })).toBeInTheDocument();
    expect(screen.queryByRole("region", { name: en.accounts.card.identities })).not.toBeInTheDocument();
    quiet.mockRestore();
  });
});
