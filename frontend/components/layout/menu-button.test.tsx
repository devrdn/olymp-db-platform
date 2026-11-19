import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Link from "next/link";
import { renderToString } from "react-dom/server";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

const navigation = vi.hoisted(() => ({ pathname: "/contests" }));
vi.mock("next/navigation", () => ({ usePathname: () => navigation.pathname }));

import { MenuButton } from "./menu-button";
import { installPopoverStandIn } from "./popover-test-support";

const labels = { open: "Menu", close: "Close menu" };

function menu() {
  return (
    <>
      <MenuButton labels={labels}>
        <Link href="/contests">Contests</Link>
        <Link href="/users">Accounts</Link>
        <button type="button">Language</button>
      </MenuButton>
      <p>the page</p>
      <Link href="/elsewhere">outside</Link>
    </>
  );
}

/** Keeps a link press in the test: jsdom has no router to navigate with. */
function stayOn(link: HTMLElement) {
  link.addEventListener("click", (event) => event.preventDefault());
  return link;
}

/** The burger once it says the panel is open (or closed): browsers report a toggle a task later. */
async function burger(open: boolean) {
  const button = await screen.findByRole("button", { name: open ? labels.close : labels.open, hidden: true });
  await waitFor(() => expect(button).toHaveAttribute("aria-expanded", String(open)));
  return button;
}

async function openIt() {
  await userEvent.click(screen.getByRole("button", { name: labels.open }));
  return burger(true);
}

/** Whether the page under the panel is held still, and how (globals.css reads it). */
function pageLock() {
  return document.documentElement.getAttribute("data-scroll-lock");
}

beforeEach(() => {
  navigation.pathname = "/contests";
});

describe.each([
  ["a browser with the Popover API", true],
  ["a browser from before it", false],
])("the menu button, in %s", (_, supported) => {
  let uninstall: () => void;
  beforeEach(() => {
    uninstall = installPopoverStandIn({ supported });
  });
  afterEach(() => {
    uninstall();
    vi.restoreAllMocks();
  });

  test("starts closed, naming what it opens and the panel it controls", () => {
    const { container } = render(menu());

    const button = screen.getByRole("button", { name: labels.open });
    expect(button).toHaveAttribute("aria-expanded", "false");
    const panel = container.querySelector(`#${button.getAttribute("aria-controls")}`);
    expect(panel).not.toBeNull();
    expect(panel).not.toBeVisible();
    expect(screen.queryByRole("link", { name: "Contests" })).not.toBeInTheDocument();
  });

  test("opens on a press and says how to close it", async () => {
    render(menu());

    await openIt();

    expect(screen.getByRole("link", { name: "Contests" })).toBeVisible();
  });

  test("is a target a finger can hit", () => {
    render(menu());

    expect(screen.getByRole("button", { name: labels.open }).className).toContain("size-11");
  });

  test("closes on a second press of the button", async () => {
    render(menu());

    await userEvent.click(await openIt());

    await burger(false);
    expect(screen.queryByRole("link", { name: "Contests" })).not.toBeInTheDocument();
  });

  test("closes on Escape and gives focus back to the button", async () => {
    render(menu());

    await openIt();
    screen.getByRole("link", { name: "Accounts" }).focus();
    await userEvent.keyboard("{Escape}");

    expect(await burger(false)).toHaveFocus();
  });

  test("gives focus to the button on Escape even when a tap never focused it", async () => {
    // Safari does not focus a button on a click, so the browser has nothing to restore.
    render(menu());

    await openIt();
    (document.activeElement as HTMLElement).blur();
    await userEvent.keyboard("{Escape}");

    expect(await burger(false)).toHaveFocus();
  });

  test("closes on a tap outside it", async () => {
    render(menu());

    await openIt();
    await userEvent.click(screen.getByText("the page"));

    await burger(false);
  });

  test("dims the page behind it from inside the panel, and closes on a tap there", async () => {
    const { container } = render(menu());

    const button = await openIt();
    const scrim = container.querySelector('[data-slot="app-bar-menu-scrim"]') as HTMLElement;
    expect(scrim).toHaveAttribute("aria-hidden", "true");
    expect(scrim).toHaveAttribute("tabindex", "-1");
    expect(scrim.className.split(/\s+/)).toContain("bg-overlay");
    expect(document.getElementById(button.getAttribute("aria-controls")!)).toContainElement(scrim);
    await userEvent.click(scrim);

    await burger(false);
  });

  test("holds the page under it still while it is open, and frees it once closed", async () => {
    render(menu());
    expect(pageLock()).toBeNull();

    await openIt();
    expect(pageLock()).not.toBeNull();

    await userEvent.click(screen.getByRole("button", { name: labels.close }));
    await burger(false);
    expect(pageLock()).toBeNull();
  });

  test("frees the page when it closes on Escape", async () => {
    render(menu());
    await openIt();

    await userEvent.keyboard("{Escape}");

    await burger(false);
    expect(pageLock()).toBeNull();
  });

  test("frees the page when a tap on the dim closes it", async () => {
    const { container } = render(menu());
    await openIt();

    await userEvent.click(container.querySelector('[data-slot="app-bar-menu-scrim"]') as HTMLElement);

    await burger(false);
    expect(pageLock()).toBeNull();
  });

  test("frees the page when it closes on navigation", async () => {
    const { rerender } = render(menu());
    await openIt();

    navigation.pathname = "/users";
    rerender(menu());

    await burger(false);
    expect(pageLock()).toBeNull();
  });

  test("frees the page when a resize hides the button", async () => {
    render(menu());
    await openIt();

    (screen.getByRole("button", { name: labels.close }).parentElement as HTMLElement).style.display = "none";
    fireEvent(window, new Event("resize"));

    await burger(false);
    expect(pageLock()).toBeNull();
  });

  test("frees the page when it is unmounted open", async () => {
    const { unmount } = render(menu());
    await openIt();

    unmount();

    expect(pageLock()).toBeNull();
  });

  // Hiding the scrollbar would otherwise widen the page under the dim.
  test("keeps the room of a scrollbar the page shows", async () => {
    vi.spyOn(document.documentElement, "clientWidth", "get").mockReturnValue(window.innerWidth - 11);
    render(menu());

    await openIt();

    expect(pageLock()).toBe("keep-gutter");
  });

  test("keeps no room for a scrollbar the page does not show", async () => {
    vi.spyOn(document.documentElement, "clientWidth", "get").mockReturnValue(window.innerWidth);
    render(menu());

    await openIt();

    expect(pageLock()).toBe("");
  });

  test("keeps the dim under the finger until the tap is over, so the tap never reaches the page", async () => {
    // A touch's click lands on whatever is under the finger by then.
    const { container } = render(menu());

    await openIt();
    const scrim = container.querySelector('[data-slot="app-bar-menu-scrim"]') as HTMLElement;
    fireEvent.pointerDown(scrim);
    fireEvent.pointerUp(scrim);
    await act(() => new Promise((resolve) => setTimeout(resolve, 0)));

    expect(await burger(true)).toBeInTheDocument();
    expect(scrim).toBeVisible();
  });

  test("stays open on a tap inside it that goes nowhere", async () => {
    render(menu());

    await openIt();
    await userEvent.click(screen.getByRole("button", { name: "Language" }));

    await burger(true);
  });

  test("closes once the visitor has navigated", async () => {
    const { rerender } = render(menu());
    await openIt();

    navigation.pathname = "/users";
    rerender(menu());

    await burger(false);
    expect(screen.queryByRole("link", { name: "Contests" })).not.toBeInTheDocument();
  });

  test("stays closed on coming back to the address it was opened at", async () => {
    const { rerender } = render(menu());
    await openIt();

    navigation.pathname = "/users";
    rerender(menu());
    await burger(false);
    navigation.pathname = "/contests";
    rerender(menu());

    await burger(false);
  });

  test("closes on a tap of a link to another page", async () => {
    render(menu());

    await openIt();
    await userEvent.click(stayOn(screen.getByRole("link", { name: "Accounts" })));

    await burger(false);
  });

  test("closes on Enter on the page already shown, and keeps focus on the button", async () => {
    // The address does not change, and focus must not fall to the body from a hidden link.
    render(menu());

    await openIt();
    stayOn(screen.getByRole("link", { name: "Contests" })).focus();
    await userEvent.keyboard("{Enter}");

    expect(await burger(false)).toHaveFocus();
  });

  test("lets Tab reach every item in order, after the button", async () => {
    render(menu());

    await openIt();
    await userEvent.tab();
    expect(screen.getByRole("link", { name: "Contests" })).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("link", { name: "Accounts" })).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("button", { name: "Language" })).toHaveFocus();
  });

  test("closes when focus moves on past it, so the panel never covers the focused element", async () => {
    render(menu());

    await openIt();
    screen.getByRole("button", { name: "Language" }).focus();
    await userEvent.tab();

    expect(screen.getByRole("link", { name: "outside" })).toHaveFocus();
    await burger(false);
  });

  test("closes when the window widens past the point where the button is hidden", async () => {
    render(menu());
    await openIt();

    fireEvent(window, new Event("resize"));
    await burger(true);

    // What the breakpoint class does to the button's wrapper.
    (screen.getByRole("button", { name: labels.close }).parentElement as HTMLElement).style.display = "none";
    fireEvent(window, new Event("resize"));

    await burger(false);
  });
});

describe("the menu button, closed by the browser itself", () => {
  let uninstall: () => void;
  beforeEach(() => {
    uninstall = installPopoverStandIn();
  });
  afterEach(() => uninstall());

  // Light dismiss and the browser's own Escape close the popover with no call
  // from the page; only the toggle event says so.
  test("frees the page when the browser closes the panel on its own", async () => {
    const { container } = render(menu());
    const button = await openIt();

    act(() => {
      (container.ownerDocument.getElementById(button.getAttribute("aria-controls")!) as HTMLElement).hidePopover();
    });

    await burger(false);
    expect(pageLock()).toBeNull();
  });
});

describe("the menu button before hydration", () => {
  let uninstall: () => void;
  beforeEach(() => {
    uninstall = installPopoverStandIn();
  });
  afterEach(() => uninstall());

  test("opens and closes without JavaScript, through the browser's popover", () => {
    // Escape and a tap outside close it there too.
    const html = renderToString(menu());
    const document = new DOMParser().parseFromString(html, "text/html");

    const button = document.querySelector("button")!;
    const panel = document.getElementById(button.getAttribute("popovertarget") ?? "");
    expect(panel?.getAttribute("popover")).toBe("auto");
    expect(button.getAttribute("aria-controls")).toBe(panel?.id);
    const scrim = panel?.querySelector('[data-slot="app-bar-menu-scrim"]');
    expect(scrim?.getAttribute("popovertarget")).toBe(panel?.id);
    expect(scrim?.getAttribute("popovertargetaction")).toBe("hide");
  });

  test("takes over a panel the browser opened before the page was hydrated", async () => {
    const container = document.createElement("div");
    document.body.append(container);
    container.innerHTML = renderToString(menu());
    const button = container.querySelector("button")!;
    await act(async () => {
      button.click();
      // The browser's toggle event fires and is gone before any listener exists.
      await new Promise((resolve) => setTimeout(resolve, 0));
    });

    const { rerender } = render(menu(), { container, hydrate: true });

    await burger(true);
    navigation.pathname = "/users";
    rerender(menu());

    await burger(false);
    container.remove();
  });
});
