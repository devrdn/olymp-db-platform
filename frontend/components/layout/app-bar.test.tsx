import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// Both switchers submit to Server Actions; importing those for real pulls in
// `next/headers`. The bar's own composition is what is under test.
vi.mock("./locale-actions", () => ({ chooseLocale: vi.fn() }));
vi.mock("./theme-actions", () => ({ chooseTheme: vi.fn() }));

import { AdminShell } from "./admin-shell";
import { FocusShell } from "./focus-shell";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

/**
 * A mark that navigates somewhere is a promise that the somewhere exists.
 * `/` has no page: the public landing is a later step, and until it is built a
 * link there is a 404 with the product's name on it.
 */
describe("the product mark", () => {
  test("is a link to the constructor for a signed-in visitor", () => {
    render(
      <AdminShell locale="en" theme="system" dict={en} section={en.contests.heading}>
        <p>rows</p>
      </AdminShell>,
    );

    expect(screen.getByRole("link", { name: en.chrome.product })).toHaveAttribute(
      "href",
      "/contests",
    );
  });

  test("is not a link on the sign-in screen, where there is nowhere to go yet", () => {
    render(
      <FocusShell locale="en" theme="system" dict={en}>
        <p>form</p>
      </FocusShell>,
    );

    expect(screen.getByText(en.chrome.product)).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});

describe("the app bar", () => {
  test("names the section a signed-in visitor is in", () => {
    render(
      <AdminShell locale="en" theme="system" dict={en} section={en.contests.heading}>
        <p>rows</p>
      </AdminShell>,
    );

    expect(screen.getByRole("banner")).toHaveTextContent(en.contests.heading);
  });

  test("offers the language the visitor is not already reading", () => {
    render(
      <FocusShell locale="en" theme="system" dict={en}>
        <p>form</p>
      </FocusShell>,
    );

    expect(screen.getByRole("button", { name: "ro" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "en" })).toHaveAttribute("aria-current", "true");
  });

  test("says where the theme control leads, not where it is", () => {
    render(
      <FocusShell locale="en" theme="system" dict={en}>
        <p>form</p>
      </FocusShell>,
    );

    // Standing on `system`, the press moves to `light`.
    expect(screen.getByRole("button", { name: en.chrome.theme.light })).toBeInTheDocument();
  });
});
