import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// Both switchers submit to Server Actions; importing those for real pulls in
// `next/headers`. What the footer puts on the page is what is under test.
vi.mock("@/components/layout/locale-actions", () => ({ chooseLocale: vi.fn() }));
vi.mock("@/components/layout/theme-actions", () => ({ chooseTheme: vi.fn() }));

import { SiteFooter } from "./site-footer";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

describe("the footer", () => {
  test("names the installation and offers the same language and theme controls as the bar", () => {
    render(<SiteFooter name="Olymp Database System" contact="admin@example.edu" locale="en" theme="system" dict={en} />);

    expect(screen.getByText("Olymp Database System")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /admin@example\.edu/ })).toHaveAttribute("href", "mailto:admin@example.edu");
    // Both are the bar's own components, so they are asserted the way the bar
    // renders them: the language switcher is a form of submit buttons, one per
    // language code, named by `chrome.language`; the theme control is a single
    // button that says where the press leads, not where it is.
    expect(screen.getByRole("form", { name: en.chrome.language })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: en.chrome.theme.light })).toBeInTheDocument();
  });

  /**
   * The contact is one free-text setting row, and an installation may put a
   * room or a telephone in it. Linking that to a mail client is a promise the
   * link cannot keep; hiding it would take away the only way to reach anybody.
   */
  test("shows a contact that is not an address as the text it is", () => {
    render(<SiteFooter name="X" contact="Block C, room 214" locale="en" theme="system" dict={en} />);

    expect(screen.getByText("Block C, room 214")).toBeInTheDocument();
    expect(screen.queryByRole("link")).toBeNull();
  });

  test("keeps quiet about a contact the installation never set", () => {
    render(<SiteFooter name="X" contact="" locale="en" theme="system" dict={en} />);
    expect(screen.queryByRole("link", { name: /mailto/ })).toBeNull();
  });
});
