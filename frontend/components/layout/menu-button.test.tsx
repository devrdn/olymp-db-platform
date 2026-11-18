import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import Link from "next/link";
import { beforeEach, describe, expect, test, vi } from "vitest";

const navigation = vi.hoisted(() => ({ pathname: "/contests" }));
vi.mock("next/navigation", () => ({ usePathname: () => navigation.pathname }));

import { MenuButton } from "./menu-button";

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

  test("closes on a tap of a link, even one to the page already shown", async () => {
    // Pressing the current destination changes no pathname, and the page must
    // not stay hidden under the panel.
    render(menu());

    await userEvent.click(screen.getByRole("button", { name: labels.open }));
    const link = screen.getByRole("link", { name: "Contests" });
    link.addEventListener("click", (event) => event.preventDefault());
    await userEvent.click(link);

    expect(screen.getByRole("button", { name: labels.open })).toHaveAttribute("aria-expanded", "false");
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
});
