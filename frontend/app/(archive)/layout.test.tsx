import { beforeEach, describe, expect, test, vi } from "vitest";

const { fetchIdentity, branding, notFound } = vi.hoisted(() => ({
  notFound: vi.fn(() => {
    throw new Error("NEXT_NOT_FOUND");
  }),
  fetchIdentity: vi.fn(),
  branding: vi.fn(),
}));
vi.mock("@/lib/auth/session", () => ({ fetchIdentity }));
vi.mock("@/lib/api/branding", () => ({ branding }));
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  notFound,
}));

vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});
vi.mock("@/lib/theme/server", () => ({ activeTheme: async () => "light" }));

import { Frame } from "@/components/layout/frame";

import ArchiveLayout from "./layout";

/** The layout and then the frame it returns, as Next renders them in one request. */
async function serve(children: React.ReactNode) {
  const element = await ArchiveLayout({ children });
  return Frame(element.props as Parameters<typeof Frame>[0]);
}

beforeEach(() => {
  vi.clearAllMocks();
  fetchIdentity.mockResolvedValue({ id: "u", login: "u", fullName: "U", roles: [], permissions: [], managesContests: false });
});

describe("the archive's layout", () => {
  test("answers not found while the archive is off, before the frame asks who this is", async () => {
    branding.mockResolvedValue({ name: "", images: {}, archive: false });

    await expect(serve(<p>inside</p>)).rejects.toThrow("NEXT_NOT_FOUND");
    expect(notFound).toHaveBeenCalled();
    expect(fetchIdentity).not.toHaveBeenCalled();
  });

  test("wears the archive's frame while the archive is on", async () => {
    branding.mockResolvedValue({ name: "", images: {}, archive: true });
    const children = <p>inside</p>;

    expect(await ArchiveLayout({ children })).toEqual(<Frame audience="archive">{children}</Frame>);
  });
});
