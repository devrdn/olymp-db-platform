import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The switchers' Server Actions would pull in `next/headers`.
vi.mock("./locale-actions", () => ({ chooseLocale: vi.fn() }));
vi.mock("./theme-actions", () => ({ chooseTheme: vi.fn() }));
vi.mock("./session-actions", () => ({ signOutAction: vi.fn() }));

import { FocusShell } from "./focus-shell";
import { ProductShell } from "./product-shell";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

describe("the product mark", () => {
  test("leads to the home of whichever audience the shell was built for", () => {
    render(
      <ProductShell locale="en" theme="system" dict={en} home="/contests" section={en.contests.heading}>
        <p>rows</p>
      </ProductShell>,
    );

    expect(screen.getByRole("link", { name: en.chrome.product })).toHaveAttribute(
      "href",
      "/contests",
    );
  });

  /** A participant sent to the author's register would find it empty. */
  test("leads somewhere else for a participant", () => {
    render(
      <ProductShell
        locale="en"
        theme="system"
        dict={en}
        home="/my"
        section={en.participant.mine.heading}
      >
        <p>rows</p>
      </ProductShell>,
    );

    expect(screen.getByRole("link", { name: en.chrome.product })).toHaveAttribute("href", "/my");
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
      <ProductShell locale="en" theme="system" dict={en} home="/contests" section={en.contests.heading}>
        <p>rows</p>
      </ProductShell>,
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

  test("names the account and leads to its profile", () => {
    // Also answers "who am I signed in as" on a shared lab machine.
    render(
      <ProductShell
        locale="en"
        theme="system"
        dict={en}
        home="/contests"
        section={en.contests.heading}
        account={{ fullName: "Ivan Ivanov", login: "ivanov" }}
      >
        <p>rows</p>
      </ProductShell>,
    );

    expect(screen.getByRole("link", { name: "Ivan Ivanov" })).toHaveAttribute("href", "/profile");
  });

  test("puts initials in the circle, and does not read them out", () => {
    // SPEC 10.4: initials, never a photograph. Decorative, since the link
    // already carries the name.
    const { container } = render(
      <ProductShell
        locale="en"
        theme="system"
        dict={en}
        home="/contests"
        account={{ fullName: "Ivan Ivanov", login: "ivanov" }}
      >
        <p>rows</p>
      </ProductShell>,
    );

    expect(container.querySelector("header [aria-hidden='true']")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Ivan Ivanov" })).toHaveAccessibleName("Ivan Ivanov");
  });

  test("offers no account door on the sign-in screen", () => {
    render(
      <FocusShell locale="en" theme="system" dict={en}>
        <p>form</p>
      </FocusShell>,
    );

    expect(screen.queryByRole("link", { name: /profile/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: en.chrome.signOut })).not.toBeInTheDocument();
  });

  test("keeps the way out on a screen that cannot reach the profile", () => {
    // The forced password change: /profile would answer 403, so sign-out must
    // be in the bar.
    render(
      <FocusShell locale="en" theme="system" dict={en} signedIn>
        <p>form</p>
      </FocusShell>,
    );

    expect(screen.getByRole("button", { name: en.chrome.signOut })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /profile/i })).not.toBeInTheDocument();
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

  // One rule here, so no route has to remember to hide the bar when printing.
  test("hides itself when the page it sits on is printed", () => {
    const { container } = render(
      <ProductShell locale="en" theme="system" dict={en} home="/contests" section={en.contests.heading}>
        <p>rows</p>
      </ProductShell>,
    );

    expect(container.querySelector("header")?.className).toMatch(/(^|\s)print:hidden(\s|$)/);
  });
});
