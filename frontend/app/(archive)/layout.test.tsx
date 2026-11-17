import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, test, vi } from "vitest";

import { getDictionary } from "@/lib/i18n/dictionary";

const { fetchIdentity, branding, redirect, notFound } = vi.hoisted(() => ({
  notFound: vi.fn(() => {
    throw new Error("NEXT_NOT_FOUND");
  }),
  fetchIdentity: vi.fn(),
  branding: vi.fn(),
  redirect: vi.fn((target: string) => {
    throw new Error(`NEXT_REDIRECT ${target}`);
  }),
}));
vi.mock("@/lib/auth/session", () => ({ fetchIdentity }));
vi.mock("@/lib/api/branding", () => ({ branding }));
vi.mock("next/navigation", () => ({ redirect, notFound, usePathname: () => "/archive" }));
vi.mock("@/lib/i18n/server", async () => {
  const { getDictionary } = await import("@/lib/i18n/dictionary");
  return { activeLocale: async () => "en", activeDictionary: async () => getDictionary("en") };
});
vi.mock("@/lib/theme/server", () => ({ activeTheme: async () => "light" }));
vi.mock("@/components/layout/product-shell", () => ({
  ProductShell: (props: { home: string; nav: React.ReactNode; children: React.ReactNode }) => (
    <div data-home={props.home}>
      {props.nav}
      {props.children}
    </div>
  ),
}));

import ArchiveLayout from "./layout";

beforeEach(() => {
  vi.clearAllMocks();
  branding.mockResolvedValue({ name: "", images: {}, archive: true });
});

describe("the archive's frame", () => {
  test("leaves a dead session to the page, which knows the address to come back to", async () => {
    fetchIdentity.mockResolvedValue(null);
    render(await ArchiveLayout({ children: <p>inside</p> }));

    expect(redirect).not.toHaveBeenCalled();
    expect(screen.getByText("inside")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Archive" })).not.toBeInTheDocument();
  });

  test("answers not found while the archive is off, even without a session", async () => {
    branding.mockResolvedValue({ name: "", images: {}, archive: false });
    fetchIdentity.mockResolvedValue(null);
    await expect(ArchiveLayout({ children: <p>inside</p> })).rejects.toThrow("NEXT_NOT_FOUND");
    expect(redirect).not.toHaveBeenCalled();
    expect(fetchIdentity).not.toHaveBeenCalled();
  });

  test("gives a participant their own menu and home, with the archive among it", async () => {
    const en = await getDictionary("en");
    fetchIdentity.mockResolvedValue({ id: "u", login: "u", fullName: "U", roles: [], permissions: [] });
    const { container } = render(await ArchiveLayout({ children: <p>inside</p> }));

    expect(container.firstElementChild).toHaveAttribute("data-home", "/my");
    expect(screen.getByRole("link", { name: en.participant.mine.heading })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: en.archive.heading })).toHaveAttribute("href", "/archive");
    expect(screen.getByText("inside")).toBeInTheDocument();
  });

  test("gives staff theirs", async () => {
    const en = await getDictionary("en");
    fetchIdentity.mockResolvedValue({ id: "u", login: "u", fullName: "U", roles: [], permissions: ["contest.create"] });
    const { container } = render(await ArchiveLayout({ children: <p>inside</p> }));

    expect(container.firstElementChild).toHaveAttribute("data-home", "/contests");
    expect(screen.getByRole("link", { name: en.contests.heading })).toBeInTheDocument();
  });

  test("answers not found while the archive is off, before any page streams", async () => {
    branding.mockResolvedValue({ name: "", images: {}, archive: false });
    fetchIdentity.mockResolvedValue({ id: "u", login: "u", fullName: "U", roles: [], permissions: [] });
    await expect(ArchiveLayout({ children: <p>inside</p> })).rejects.toThrow("NEXT_NOT_FOUND");
    expect(notFound).toHaveBeenCalled();
  });
});
