import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Link from "next/link";
import { renderToString } from "react-dom/server";
import { afterAll, beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

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

let uninstall: () => void;
beforeAll(() => {
  uninstall = installPopoverStandIn();
});
afterAll(() => uninstall());

beforeEach(() => {
  navigation.pathname = "/contests";
});

describe("the menu button", () => {
  test("starts closed, naming what it opens and the panel it controls", () => {
    const { container } = render(menu());

    const button = screen.getByRole("button", { name: labels.open });
    expect(button).toHaveAttribute("aria-expanded", "false");
    const panel = container.querySelector(`#${button.getAttribute("aria-controls")}`);
    expect(panel).not.toBeNull();
    expect(panel).not.toBeVisible();
    expect(screen.queryByRole("link", { name: "Contests" })).not.toBeInTheDocument();
  });

  test("opens and closes without JavaScript, through the browser's popover", () => {
    // Before hydration, or with scripts off, the browser toggles the panel, and
    // closes it on Escape and on a tap outside.
    const html = renderToString(menu());
    const document = new DOMParser().parseFromString(html, "text/html");

    const button = document.querySelector("button")!;
    const panel = document.getElementById(button.getAttribute("popovertarget") ?? "");
    expect(panel?.getAttribute("popover")).toBe("auto");
    expect(button.getAttribute("aria-controls")).toBe(panel?.id);
  });

  test("opens on a press and says how to close it", async () => {
    render(menu());

    await userEvent.click(screen.getByRole("button", { name: labels.open }));

    const button = screen.getByRole("button", { name: labels.close });
    expect(button).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByRole("link", { name: "Contests" })).toBeVisible();
  });

  test("is a target a finger can hit", () => {
    render(menu());

    expect(screen.getByRole("button", { name: labels.open }).className).toContain("size-11");
  });

  test("closes on a second press of the button", async () => {
    render(menu());

    await userEvent.click(screen.getByRole("button", { name: labels.open }));
    await userEvent.click(screen.getByRole("button", { name: labels.close }));

    expect(screen.getByRole("button", { name: labels.open })).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("link", { name: "Contests" })).not.toBeInTheDocument();
  });

  test("closes on Escape and gives focus back to the button", async () => {
    render(menu());

    await userEvent.click(screen.getByRole("button", { name: labels.open }));
    screen.getByRole("link", { name: "Accounts" }).focus();
    await userEvent.keyboard("{Escape}");

    const button = screen.getByRole("button", { name: labels.open });
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(button).toHaveFocus();
  });

  test("gives focus to the button on Escape even when a tap never focused it", async () => {
    // Safari does not focus a button on a click, so the browser has nothing to restore.
    render(menu());

    await userEvent.click(screen.getByRole("button", { name: labels.open }));
    (document.activeElement as HTMLElement).blur();
    await userEvent.keyboard("{Escape}");

    expect(screen.getByRole("button", { name: labels.open })).toHaveFocus();
  });

  test("closes on a tap outside it", async () => {
    render(menu());

    await userEvent.click(screen.getByRole("button", { name: labels.open }));
    await userEvent.click(screen.getByText("the page"));

    expect(screen.getByRole("button", { name: labels.open })).toHaveAttribute("aria-expanded", "false");
  });

  test("stays open on a tap inside it that goes nowhere", async () => {
    render(menu());

    await userEvent.click(screen.getByRole("button", { name: labels.open }));
    await userEvent.click(screen.getByRole("button", { name: "Language" }));

    expect(screen.getByRole("button", { name: labels.close })).toHaveAttribute("aria-expanded", "true");
  });

  test("closes once the visitor has navigated", async () => {
    const { rerender } = render(menu());
    await userEvent.click(screen.getByRole("button", { name: labels.open }));

    navigation.pathname = "/users";
    rerender(menu());

    expect(screen.getByRole("button", { name: labels.open })).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("link", { name: "Contests" })).not.toBeInTheDocument();
  });

  test("stays closed on coming back to the address it was opened at", async () => {
    const { rerender } = render(menu());
    await userEvent.click(screen.getByRole("button", { name: labels.open }));

    navigation.pathname = "/users";
    rerender(menu());
    navigation.pathname = "/contests";
    rerender(menu());

    expect(screen.getByRole("button", { name: labels.open })).toHaveAttribute("aria-expanded", "false");
  });

  test("closes on a tap of a link to another page", async () => {
    render(menu());

    await userEvent.click(screen.getByRole("button", { name: labels.open }));
    await userEvent.click(stayOn(screen.getByRole("link", { name: "Accounts" })));

    expect(screen.getByRole("button", { name: labels.open })).toHaveAttribute("aria-expanded", "false");
  });

  test("closes on Enter on the page already shown, and keeps focus on the button", async () => {
    // The address does not change, and focus must not fall to the body from a hidden link.
    render(menu());

    await userEvent.click(screen.getByRole("button", { name: labels.open }));
    stayOn(screen.getByRole("link", { name: "Contests" })).focus();
    await userEvent.keyboard("{Enter}");

    const button = screen.getByRole("button", { name: labels.open });
    expect(button).toHaveAttribute("aria-expanded", "false");
    expect(button).toHaveFocus();
  });

  test("lets Tab reach every item in order, after the button", async () => {
    render(menu());

    await userEvent.click(screen.getByRole("button", { name: labels.open }));
    await userEvent.tab();
    expect(screen.getByRole("link", { name: "Contests" })).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("link", { name: "Accounts" })).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("button", { name: "Language" })).toHaveFocus();
  });

  test("closes when focus moves on past it, so the panel never covers the focused element", async () => {
    render(menu());

    await userEvent.click(screen.getByRole("button", { name: labels.open }));
    screen.getByRole("button", { name: "Language" }).focus();
    await userEvent.tab();

    expect(screen.getByRole("link", { name: "outside" })).toHaveFocus();
    expect(screen.getByRole("button", { name: labels.open })).toHaveAttribute("aria-expanded", "false");
  });

  test("closes when the window widens past the point where the button is hidden", async () => {
    render(menu());
    await userEvent.click(screen.getByRole("button", { name: labels.open }));

    fireEvent(window, new Event("resize"));
    expect(screen.getByRole("button", { name: labels.close })).toHaveAttribute("aria-expanded", "true");

    // What the breakpoint class does to the button's wrapper.
    (screen.getByRole("button", { name: labels.close }).parentElement as HTMLElement).style.display = "none";
    fireEvent(window, new Event("resize"));

    expect(screen.getByRole("button", { name: labels.open, hidden: true })).toHaveAttribute("aria-expanded", "false");
  });
});
