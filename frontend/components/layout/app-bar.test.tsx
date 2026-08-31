import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// Both switchers submit to Server Actions; importing those for real pulls in
// `next/headers`. The bar's own composition is what is under test.
vi.mock("./locale-actions", () => ({ chooseLocale: vi.fn() }));
vi.mock("./theme-actions", () => ({ chooseTheme: vi.fn() }));
vi.mock("./session-actions", () => ({ signOutAction: vi.fn() }));

import { FocusShell } from "./focus-shell";
import { ProductShell } from "./product-shell";

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

  /**
   * The whole reason there is one shell and not two. A participant sent to the
   * author's register would meet it scoped to contests they manage, which is
   * empty — an accurate answer to a question they never asked.
   */
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
    // The way into one's own account, and the answer to "who am I signed in
    // as" — which on a shared lab machine is a question people actually ask
    // before they type anything.
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
    // SPEC 10.4: initials, never a photograph. The letters are decoration —
    // the link already carries the name, and "I I" beside it would be noise.
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
    // There is no account yet, and a door to nobody's profile is one that
    // invites a press to find out.
    render(
      <FocusShell locale="en" theme="system" dict={en}>
        <p>form</p>
      </FocusShell>,
    );

    expect(screen.queryByRole("link", { name: /profile/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: en.chrome.signOut })).not.toBeInTheDocument();
  });

  test("keeps the way out on a screen that cannot reach the profile", () => {
    // The forced password change. That account is signed in and the API
    // refuses every endpoint but three, so /profile would answer 403 — and
    // without a control here the only escape from somebody else's handover
    // password is clearing a cookie by hand.
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
});
