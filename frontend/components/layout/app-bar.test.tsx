import { readFileSync } from "node:fs";
import { join } from "node:path";

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterAll, beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The language and sign-out Server Actions would pull in `next/headers`.
vi.mock("./locale-actions", () => ({ chooseLocale: vi.fn() }));
vi.mock("./session-actions", () => ({ signOutAction: vi.fn() }));

vi.mock("next/navigation", () => ({ usePathname: () => "/contests" }));

import { AppBar } from "./app-bar";
import { installPopoverStandIn } from "./popover-test-support";

let en: Dictionary;
let uninstall: () => void;

beforeAll(async () => {
  en = await getDictionary("en");
  uninstall = installPopoverStandIn();
});
afterAll(() => uninstall());

describe("the product mark", () => {
  test("leads to the home of whichever audience the bar was built for", () => {
    render(
      <AppBar locale="en" theme="system" dict={en} home="/contests" />,
    );

    expect(screen.getByRole("link", { name: en.chrome.product })).toHaveAttribute(
      "href",
      "/contests",
    );
  });

  /** A participant sent to the author's register would find it empty. */
  test("leads somewhere else for a participant", () => {
    render(
      <AppBar locale="en" theme="system" dict={en} home="/my" />,
    );

    expect(screen.getByRole("link", { name: en.chrome.product })).toHaveAttribute("href", "/my");
  });

  test("shows the product's logo until the installation uploads its own", () => {
    const { container, rerender } = render(
      <AppBar locale="en" theme="system" dict={en} home="/contests" />,
    );
    const link = screen.getByRole("link", { name: en.chrome.product });
    expect(link.querySelector('svg[data-slot="logo"]')).not.toBeNull();

    rerender(
      <AppBar locale="en" theme="system" dict={en} home="/contests" logo="/api/v1/settings/images/logo/abc" />,
    );
    expect(container.querySelector('svg[data-slot="logo"]')).toBeNull();
    expect(container.querySelector('header img[src="/api/v1/settings/images/logo/abc"]')).not.toBeNull();
  });

  describe("for the dark theme", () => {
    const LIGHT = "/api/v1/settings/images/logo?v=l1";
    const DARK = "/api/v1/settings/images/logo-dark?v=d1";
    const bar = (props: { logo?: string; logoDark?: string }) =>
      render(
        <AppBar locale="en" theme="system" dict={en} home="/contests" {...props} />,
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
      <AppBar locale="en" theme="system" dict={en} />,
    );

    expect(screen.getByText(en.chrome.product)).toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });
});

describe("the app bar", () => {
  test("offers the language the visitor is not already reading", () => {
    render(
      <AppBar locale="en" theme="system" dict={en} />,
    );

    expect(screen.getByRole("button", { name: "ro" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "en" })).toHaveAttribute("aria-current", "true");
  });

  test("names the account and leads to its profile", () => {
    // Also answers "who am I signed in as" on a shared lab machine.
    render(
      <AppBar locale="en" theme="system" dict={en} home="/contests" account={{ fullName: "Ivan Ivanov", login: "ivanov" }} />,
    );

    expect(screen.getByRole("link", { name: "Ivan Ivanov" })).toHaveAttribute("href", "/profile");
  });

  test("puts initials in the circle, and does not read them out", () => {
    // SPEC 10.4: initials, never a photograph. Decorative, since the link
    // already carries the name.
    const { container } = render(
      <AppBar locale="en" theme="system" dict={en} home="/contests" account={{ fullName: "Ivan Ivanov", login: "ivanov" }} />,
    );

    expect(container.querySelector("header [aria-hidden='true']")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Ivan Ivanov" })).toHaveAccessibleName("Ivan Ivanov");
  });

  test("offers no account door on the sign-in screen", () => {
    render(
      <AppBar locale="en" theme="system" dict={en} />,
    );

    expect(screen.queryByRole("link", { name: /profile/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: en.chrome.signOut })).not.toBeInTheDocument();
  });

  test("keeps the way out on a screen that cannot reach the profile", () => {
    // The forced password change: /profile would answer 403, so sign-out must
    // be in the bar.
    render(
      <AppBar locale="en" theme="system" dict={en} signedIn />,
    );

    expect(screen.getByRole("button", { name: en.chrome.signOut })).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /profile/i })).not.toBeInTheDocument();
  });

  test("says where the theme control leads, not where it is", () => {
    render(
      <AppBar locale="en" theme="system" dict={en} />,
    );

    // Standing on `system`, the press moves to `light`.
    expect(screen.getByRole("button", { name: en.chrome.theme.light })).toBeInTheDocument();
  });

  // One rule here, so no route has to remember to hide the bar when printing.
  test("hides itself when the page it sits on is printed", () => {
    const { container } = render(
      <AppBar locale="en" theme="system" dict={en} home="/contests" />,
    );

    expect(container.querySelector("header")?.className).toMatch(/(^|\s)print:hidden(\s|$)/);
  });
});

const SHORT = [
  { href: "/contests", label: "Contests" },
  { href: "/users", label: "Accounts" },
  { href: "/audit", label: "Audit trail" },
];
/** Everything a staff account with every permission sees. */
const CROWDED = [
  { href: "/contests", label: "Contests" },
  { href: "/archive", label: "Archive" },
  { href: "/users", label: "Accounts" },
  { href: "/registrations", label: "Registrations" },
  { href: "/audit", label: "Audit trail" },
  { href: "/settings", label: "Installation" },
  { href: "/integrations", label: "Integrations" },
];
const ACCOUNT = { fullName: "Ivan Ivanov", login: "ivanov" };

function staffBar(props: { name?: string; destinations?: { href: string; label: string }[] } = {}) {
  return render(
    <AppBar locale="en" theme="system" dict={en} home="/contests" destinations={SHORT} account={ACCOUNT} {...props} />,
  );
}

/** Whether a class list carries this exact utility, not merely a longer one that contains it. */
function hasClass(element: Element | null | undefined, utility: string) {
  return (element?.className ?? "").split(/\s+/).includes(utility);
}

/** The open panel's scrolling body, where its contents live. */
async function openMenu() {
  await userEvent.click(screen.getByRole("button", { name: en.chrome.menu.open }));
  const panel = (await screen.findByRole("button", { name: en.chrome.menu.close })).nextElementSibling as HTMLElement;
  // After the dim, which is the panel's first child.
  return panel.lastElementChild as HTMLElement;
}

/**
 * The breakpoint each part of the bar switches at. The row, its divider and the
 * controls hide below it and the menu from it, so exactly one bar shows at every width.
 */
function foldPoints(container: HTMLElement) {
  const below = (slot: string) => {
    const element = container.querySelector(`[data-slot="${slot}"]`);
    return ["wide", "wider"].filter((point) => hasClass(element, `max-${point}:hidden`));
  };
  const menu = container.querySelector('[data-slot="app-bar-menu"]');
  return {
    row: below("app-bar-nav"),
    divider: below("app-bar-divider"),
    controls: below("app-bar-controls"),
    menu: ["wide", "wider"].filter((point) => hasClass(menu, `${point}:hidden`)),
  };
}

describe("the bar from 1024px up", () => {
  test("lists the destinations in a row between the mark and the controls", () => {
    const { container } = staffBar();

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
    const { container } = staffBar();

    const controls = container.querySelector('[data-slot="app-bar-controls"]') as HTMLElement;
    expect(within(controls).getByRole("button", { name: en.chrome.theme.light })).toBeInTheDocument();
    expect(within(controls).getByRole("button", { name: "ro" })).toBeInTheDocument();
    expect(within(controls).getByRole("link", { name: "Ivan Ivanov" })).toHaveAttribute("href", "/profile");
  });

  test("shows a row of up to three destinations from 1024px, and the menu only below", () => {
    // CSS rather than measurement, so the server-rendered bar is right before hydration.
    const { container } = staffBar({ destinations: SHORT });

    expect(foldPoints(container)).toEqual({ row: ["wide"], divider: ["wide"], controls: ["wide"], menu: ["wide"] });
  });

  test("keeps four destinations in the menu at 1024px, where they were measured not to fit", () => {
    const four = [...SHORT, { href: "/registrations", label: "Registrations" }];
    const { container } = staffBar({ destinations: four });

    expect(foldPoints(container)).toEqual({ row: ["wider"], divider: ["wider"], controls: ["wider"], menu: ["wider"] });
  });

  test("keeps a crowded bar in the menu until its row fits, and only then shows the row", () => {
    const { container } = staffBar({ destinations: CROWDED });

    expect(foldPoints(container)).toEqual({ row: ["wider"], divider: ["wider"], controls: ["wider"], menu: ["wider"] });
    expect(hasClass(container.querySelector('[data-slot="app-bar-menu"]')?.parentElement?.firstElementChild, "max-wider:grow")).toBe(true);
  });

  test("folds a crowded bar at a breakpoint the stylesheet defines, no wider than 90rem", () => {
    // Tailwind emits nothing for a variant with no breakpoint behind it, and says nothing either.
    const css = readFileSync(join(__dirname, "..", "..", "app", "globals.css"), "utf8");
    const wider = /--breakpoint-wider:\s*([\d.]+)rem;/.exec(css);
    const wide = /--breakpoint-wide:\s*([\d.]+)rem;/.exec(css);

    expect(wider).not.toBeNull();
    expect(Number(wider![1])).toBeGreaterThan(Number(wide![1]));
    expect(Number(wider![1])).toBeLessThanOrEqual(90);
  });
});

describe("the bar below its row", () => {
  test("offers a menu button, closed, named from the dictionary", () => {
    staffBar();

    const button = screen.getByRole("button", { name: en.chrome.menu.open });
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(button).toHaveAttribute("aria-controls");
  });

  test("puts the mark and the button on one row of the narrow bar's height, which cannot wrap", () => {
    const { container } = staffBar();

    const menu = container.querySelector('[data-slot="app-bar-menu"]');
    const bar = menu?.parentElement as HTMLElement;
    expect(hasClass(bar, "h-12")).toBe(true);
    expect(hasClass(bar, "max-narrow:min-h-12")).toBe(true);
    // Below 760px the bar of a screen without a menu wraps its controls onto a
    // second row; with only the mark and the button there is nothing to wrap.
    expect(bar.className).not.toMatch(/(^|\s)max-narrow:flex-wrap(\s|$)/);
    // The mark takes what the button leaves.
    expect(hasClass(bar.firstElementChild, "max-wide:grow")).toBe(true);
    expect(hasClass(menu, "shrink-0")).toBe(true);
  });

  // The panel hangs at `top-12`, which is right only while the bar is one row.
  test("never lets a folded bar wrap, at any width, whatever the number of destinations", () => {
    for (const destinations of [SHORT, CROWDED]) {
      const { container, unmount } = staffBar({ destinations });

      const bar = container.querySelector('[data-slot="app-bar-menu"]')?.parentElement as HTMLElement;
      expect(bar.className.split(/\s+/).filter((utility) => /(^|:)flex-wrap$/.test(utility))).toEqual([]);
      unmount();
    }
  });

  test("opens onto every destination, with the current one marked", async () => {
    staffBar();

    const panel = await openMenu();
    const links = within(within(panel).getByRole("navigation")).getAllByRole("link");
    expect(links.map((a) => a.textContent)).toEqual(["Contests", "Accounts", "Audit trail"]);
    expect(links[0]).toHaveAttribute("aria-current", "page");
    expect(links[1]).not.toHaveAttribute("aria-current");
    // Full-width rows a finger can hit.
    for (const link of links) expect(hasClass(link, "min-h-12")).toBe(true);
  });

  test("holds, after a separator, the account and then the theme and the language", async () => {
    staffBar();

    const panel = await openMenu();
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

  test("hangs under the bar, on the bar's own column, and scrolls inside what the screen leaves", async () => {
    staffBar();

    const body = await openMenu();
    const panel = body.parentElement as HTMLElement;
    // The bar is 48px and a 1px rule; the panel starts under the rule and runs to the bottom.
    for (const utility of ["fixed", "top-12", "mt-px", "bottom-0", "inset-x-0"]) expect(hasClass(panel, utility)).toBe(true);
    const bar = screen.getByRole("banner");
    const tracks = (element: Element) => element.className.split(/\s+/).filter((c) => /grid-cols-/.test(c));
    expect(tracks(panel)).toEqual(tracks(bar));
    expect(hasClass(body, "col-start-2")).toBe(true);
    expect(hasClass(body, "max-h-full")).toBe(true);
    expect(hasClass(body, "overflow-y-auto")).toBe(true);
  });

  test("has no menu on a screen without destinations", () => {
    render(
      <AppBar locale="en" theme="system" dict={en} signedIn />,
    );

    expect(screen.queryByRole("button", { name: en.chrome.menu.open })).not.toBeInTheDocument();
    // Its controls stay in the bar at every width, sign-out included.
    const controls = document.querySelector('[data-slot="app-bar-controls"]');
    expect(controls?.className).not.toMatch(/max-wider?:hidden/);
    expect(within(controls as HTMLElement).getByRole("button", { name: en.chrome.signOut })).toBeInTheDocument();
  });

  test("has no menu on a signed-in screen without destinations, and keeps its controls in the bar", () => {
    render(<AppBar locale="en" theme="system" dict={en} home="/contests" account={ACCOUNT} />);

    expect(screen.queryByRole("button", { name: en.chrome.menu.open })).not.toBeInTheDocument();
    expect(document.querySelector('[data-slot="app-bar-controls"]')?.className).not.toMatch(/max-wider?:hidden/);
  });
});

describe("the installation's name", () => {
  const LONG = "Olimpiada de baze de date a unei instituții cu un nume foarte, foarte lung";

  test("takes one line with an ellipsis from 760px up, so it cannot push anything off the screen", () => {
    staffBar({ name: LONG });

    const name = screen.getByText(LONG);
    expect(hasClass(name, "narrow:truncate")).toBe(true);
    // A flex item shrinks below its text only with a zero minimum, at every level.
    const link = screen.getByRole("link", { name: LONG });
    expect(hasClass(link, "min-w-0")).toBe(true);
    expect(hasClass(link.parentElement, "min-w-0")).toBe(true);
    expect(hasClass(link.parentElement, "shrink-0")).toBe(false);
  });

  test("gives way to the destinations, which keep their full width in the row", () => {
    const { container } = staffBar({ name: LONG });

    const row = container.querySelector('[data-slot="app-bar-nav"]');
    expect(hasClass(row, "shrink-0")).toBe(true);
    expect(hasClass(row, "grow")).toBe(true);
  });

  test("is not capped where there is no row to make room for", () => {
    render(
      <AppBar locale="en" theme="system" dict={en} name={LONG} />,
    );

    expect(screen.getByText(LONG).closest("div")?.className).not.toMatch(/max-w-/);
  });

  test("takes two lines at phone width rather than push the controls onto a row of their own", () => {
    render(
      <AppBar locale="en" theme="system" dict={en} home="/contests" name="Olimpiada de baze de date" />,
    );

    const link = screen.getByRole("link", { name: "Olimpiada de baze de date" });
    expect(link.parentElement?.className).toContain("max-narrow:basis-0");
    expect(link.parentElement?.className).toContain("max-narrow:grow");
    expect(screen.getByText("Olimpiada de baze de date").className).toContain("max-narrow:line-clamp-2");
  });
});
