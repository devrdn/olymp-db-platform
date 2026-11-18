import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The language and sign-out Server Actions would pull in `next/headers`.
vi.mock("./locale-actions", () => ({ chooseLocale: vi.fn() }));
vi.mock("./session-actions", () => ({ signOutAction: vi.fn() }));

vi.mock("next/navigation", () => ({ usePathname: () => "/contests" }));

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

  test("shows the product's logo until the installation uploads its own", () => {
    const { container, rerender } = render(
      <ProductShell locale="en" theme="system" dict={en} home="/contests">
        <p>rows</p>
      </ProductShell>,
    );
    const link = screen.getByRole("link", { name: en.chrome.product });
    expect(link.querySelector('svg[data-slot="logo"]')).not.toBeNull();

    rerender(
      <ProductShell locale="en" theme="system" dict={en} home="/contests" logo="/api/v1/settings/images/logo/abc">
        <p>rows</p>
      </ProductShell>,
    );
    expect(container.querySelector('svg[data-slot="logo"]')).toBeNull();
    expect(container.querySelector('header img[src="/api/v1/settings/images/logo/abc"]')).not.toBeNull();
  });

  describe("for the dark theme", () => {
    const LIGHT = "/api/v1/settings/images/logo?v=l1";
    const DARK = "/api/v1/settings/images/logo-dark?v=d1";
    const bar = (props: { logo?: string; logoDark?: string }) =>
      render(
        <ProductShell locale="en" theme="system" dict={en} home="/contests" {...props}>
          <p>rows</p>
        </ProductShell>,
      ).container.querySelector("header")!;

    test("shows each upload only in its own theme", () => {
      const header = bar({ logo: LIGHT, logoDark: DARK });

      expect(header.querySelectorAll("img")).toHaveLength(2);
      expect(header.querySelector(`img[src="${LIGHT}"]`)).toHaveClass("light-only");
      expect(header.querySelector(`img[src="${DARK}"]`)).toHaveClass("dark-only");
      // A lazy image with no box is never fetched, so the hidden one costs nothing.
      expect(header.querySelector(`img[src="${LIGHT}"]`)).toHaveAttribute("loading", "lazy");
      expect(header.querySelector(`img[src="${DARK}"]`)).toHaveAttribute("loading", "lazy");
    });

    test("keeps the cat in the light theme beside a dark upload", () => {
      const header = bar({ logoDark: DARK });

      expect(header.querySelector('svg[data-slot="logo"]')).toHaveClass("light-only");
      expect(header.querySelector(`img[src="${DARK}"]`)).toHaveClass("dark-only");
      expect(header.querySelector(`img[src="${DARK}"]`)).toHaveAttribute("loading", "lazy");
    });

    test("adds nothing when there is no dark upload", () => {
      const header = bar({ logo: LIGHT });

      expect(header.querySelectorAll("img")).toHaveLength(1);
      expect(header.querySelector(".light-only, .dark-only")).toBeNull();
      expect(header.querySelector("img")).not.toHaveAttribute("loading");
    });

    test("is the lone cat when nothing was uploaded", () => {
      const header = bar({});

      expect(header.querySelectorAll("img")).toHaveLength(0);
      expect(header.querySelectorAll('svg[data-slot="logo"]')).toHaveLength(1);
      expect(header.querySelector(".light-only, .dark-only")).toBeNull();
    });
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

const STAFF = [
  { href: "/contests", label: "Contests" },
  { href: "/users", label: "Accounts" },
  { href: "/audit", label: "Audit trail" },
];
const ACCOUNT = { fullName: "Ivan Ivanov", login: "ivanov" };

function staffShell(props: { name?: string } = {}) {
  return render(
    <ProductShell locale="en" theme="system" dict={en} home="/contests" destinations={STAFF} account={ACCOUNT} {...props}>
      <p>rows</p>
    </ProductShell>,
  );
}

/** Whether a class list carries this exact utility, not merely a longer one that contains it. */
function hasClass(element: Element | null | undefined, utility: string) {
  return (element?.className ?? "").split(/\s+/).includes(utility);
}

describe("the bar from 1024px up", () => {
  test("lists the destinations in a row between the mark and the controls", () => {
    const { container } = staffShell();

    const row = container.querySelector('[data-slot="app-bar-nav"]');
    expect(row).not.toBeNull();
    // The row follows the mark in the DOM, so the tab order matches the screen; the
    // controls are not inside it.
    const bar = row?.parentElement as HTMLElement;
    expect(bar.firstElementChild?.textContent).toContain(en.chrome.product);
    expect(within(row as HTMLElement).queryAllByRole("button")).toEqual([]);
    expect(within(row as HTMLElement).getAllByRole("link").map((a) => a.textContent)).toEqual([
      "Contests",
      "Accounts",
      "Audit trail",
    ]);
    expect(within(row as HTMLElement).getByRole("link", { name: "Contests" })).toHaveAttribute("aria-current", "page");
  });

  test("keeps the theme, the language and the account beside the row", () => {
    const { container } = staffShell();

    const controls = container.querySelector('[data-slot="app-bar-controls"]') as HTMLElement;
    expect(within(controls).getByRole("button", { name: en.chrome.theme.light })).toBeInTheDocument();
    expect(within(controls).getByRole("button", { name: "ro" })).toBeInTheDocument();
    expect(within(controls).getByRole("link", { name: "Ivan Ivanov" })).toHaveAttribute("href", "/profile");
  });

  test("shows the row and the controls only from 1024px, and the menu only below", () => {
    // CSS rather than measurement, so the server-rendered bar is right before hydration.
    const { container } = staffShell();

    expect(hasClass(container.querySelector('[data-slot="app-bar-nav"]'), "max-wide:hidden")).toBe(true);
    expect(hasClass(container.querySelector('[data-slot="app-bar-controls"]'), "max-wide:hidden")).toBe(true);
    expect(hasClass(container.querySelector('[data-slot="app-bar-menu"]'), "wide:hidden")).toBe(true);
  });
});

describe("the bar below 1024px", () => {
  test("offers a menu button, closed, named from the dictionary", () => {
    staffShell();

    const button = screen.getByRole("button", { name: en.chrome.menu.open });
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(button).toHaveAttribute("aria-controls");
  });

  test("puts the mark and the button on one row of the narrow bar's height", () => {
    const { container } = staffShell();

    const menu = container.querySelector('[data-slot="app-bar-menu"]');
    const bar = menu?.parentElement as HTMLElement;
    expect(hasClass(bar, "h-12")).toBe(true);
    // The mark takes what the button leaves.
    expect(hasClass(bar.firstElementChild, "max-wide:grow")).toBe(true);
    expect(hasClass(menu, "shrink-0")).toBe(true);
  });

  test("opens onto every destination, with the current one marked", async () => {
    staffShell();

    await userEvent.click(screen.getByRole("button", { name: en.chrome.menu.open }));

    const panel = screen.getByRole("button", { name: en.chrome.menu.close }).nextElementSibling as HTMLElement;
    const links = within(within(panel).getByRole("navigation")).getAllByRole("link");
    expect(links.map((a) => a.textContent)).toEqual(["Contests", "Accounts", "Audit trail"]);
    expect(links[0]).toHaveAttribute("aria-current", "page");
    expect(links[1]).not.toHaveAttribute("aria-current");
    // Full-width rows a finger can hit.
    for (const link of links) expect(hasClass(link, "min-h-12")).toBe(true);
  });

  test("holds, after a separator, the account and then the theme and the language", async () => {
    staffShell();

    await userEvent.click(screen.getByRole("button", { name: en.chrome.menu.open }));
    const panel = screen.getByRole("button", { name: en.chrome.menu.close }).nextElementSibling as HTMLElement;

    const nav = within(panel).getByRole("navigation");
    const separator = within(panel).getByRole("separator");
    const account = within(panel).getByRole("link", { name: /Ivan Ivanov/ });
    const theme = within(panel).getByRole("button", { name: en.chrome.theme.light });
    const language = within(panel).getByRole("form", { name: en.chrome.language });
    const order = [nav, separator, account, theme, language];
    for (let i = 1; i < order.length; i++) {
      expect(order[i - 1].compareDocumentPosition(order[i]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }

    // The name is written out, not only the initials, and leads to the profile.
    expect(account).toHaveAttribute("href", "/profile");
    expect(account).toHaveTextContent("Ivan Ivanov");
    // Every option at least 44px high.
    expect(hasClass(account, "min-h-12")).toBe(true);
    expect(hasClass(theme, "min-h-11")).toBe(true);
    expect(theme).toHaveTextContent(en.chrome.theme.light);
    for (const option of within(language).getAllByRole("button")) {
      expect(hasClass(option, "min-h-11")).toBe(true);
      expect(hasClass(option, "min-w-11")).toBe(true);
    }
  });

  test("scrolls inside itself when taller than what the screen leaves under the bar", async () => {
    staffShell();

    await userEvent.click(screen.getByRole("button", { name: en.chrome.menu.open }));
    const panel = screen.getByRole("button", { name: en.chrome.menu.close }).nextElementSibling as HTMLElement;

    expect(hasClass(panel, "overflow-y-auto")).toBe(true);
    expect(panel.className).toMatch(/(^|\s)max-h-\[calc\(100dvh-/);
  });

  test("has no menu on a screen without destinations", () => {
    render(
      <FocusShell locale="en" theme="system" dict={en} signedIn>
        <p>form</p>
      </FocusShell>,
    );

    expect(screen.queryByRole("button", { name: en.chrome.menu.open })).not.toBeInTheDocument();
    // Its controls stay in the bar at every width, sign-out included.
    const controls = document.querySelector('[data-slot="app-bar-controls"]');
    expect(hasClass(controls, "max-wide:hidden")).toBe(false);
    expect(within(controls as HTMLElement).getByRole("button", { name: en.chrome.signOut })).toBeInTheDocument();
  });

  test("has no menu on a signed-in screen without destinations, which names its section instead", () => {
    render(
      <ProductShell locale="en" theme="system" dict={en} home="/contests" section={en.contests.heading} account={ACCOUNT}>
        <p>rows</p>
      </ProductShell>,
    );

    expect(screen.queryByRole("button", { name: en.chrome.menu.open })).not.toBeInTheDocument();
    expect(screen.getByRole("banner")).toHaveTextContent(en.contests.heading);
    expect(hasClass(document.querySelector('[data-slot="app-bar-controls"]'), "max-wide:hidden")).toBe(false);
  });
});

describe("the installation's name", () => {
  const LONG = "Olimpiada de baze de date a unei instituții cu un nume foarte, foarte lung";

  test("takes one line with an ellipsis from 760px up, so it cannot push anything off the screen", () => {
    staffShell({ name: LONG });

    const name = screen.getByText(LONG);
    expect(hasClass(name, "narrow:truncate")).toBe(true);
    // A flex item shrinks below its text only with a zero minimum, at every level.
    const link = screen.getByRole("link", { name: LONG });
    expect(hasClass(link, "min-w-0")).toBe(true);
    expect(hasClass(link.parentElement, "min-w-0")).toBe(true);
    expect(hasClass(link.parentElement, "shrink-0")).toBe(false);
  });

  test("leaves the destinations room beside a long name from 1024px up", () => {
    staffShell({ name: LONG });

    expect(hasClass(screen.getByRole("link", { name: LONG }).parentElement, "wide:max-w-1/3")).toBe(true);
  });

  test("takes two lines at phone width rather than push the controls onto a row of their own", () => {
    render(
      <ProductShell locale="en" theme="system" dict={en} home="/contests" name="Olimpiada de baze de date">
        <p>rows</p>
      </ProductShell>,
    );

    const link = screen.getByRole("link", { name: "Olimpiada de baze de date" });
    expect(link.parentElement?.className).toContain("max-narrow:basis-0");
    expect(link.parentElement?.className).toContain("max-narrow:grow");
    expect(screen.getByText("Olimpiada de baze de date").className).toContain("max-narrow:line-clamp-2");
  });
});
