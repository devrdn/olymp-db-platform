import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderToString } from "react-dom/server";
import { afterEach, beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";
import { THEME_COOKIE } from "@/lib/theme/config";

import { ThemeToggle } from "./theme-toggle";

let labels: Dictionary["chrome"]["theme"];

beforeAll(async () => {
  labels = (await getDictionary("en")).chrome.theme;
});

afterEach(() => {
  document.documentElement.removeAttribute("data-theme");
  document.documentElement.removeAttribute("data-theme-swap");
  document.cookie = `${THEME_COOKIE}=; path=/; max-age=0`;
  vi.restoreAllMocks();
});

/** What the server stamped for this theme, as the root layout would. */
function stamped(theme: "system" | "light" | "dark") {
  if (theme === "system") document.documentElement.removeAttribute("data-theme");
  else document.documentElement.setAttribute("data-theme", theme);
}

describe("the theme toggle", () => {
  test("names where the press leads", () => {
    render(<ThemeToggle current="system" labels={labels} />);
    expect(screen.getByRole("button", { name: labels.light })).toBeInTheDocument();
  });

  test("swaps the attribute on the page itself, without a form to submit", async () => {
    render(<ThemeToggle current="system" labels={labels} />);
    const button = screen.getByRole("button", { name: labels.light });

    // No form, so nothing reaches the server and no server component renders again.
    expect(button.closest("form")).toBeNull();
    expect(button).toHaveAttribute("type", "button");

    await userEvent.click(button);
    expect(document.documentElement).toHaveAttribute("data-theme", "light");

    await userEvent.click(screen.getByRole("button", { name: labels.dark }));
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");

    // The system theme stamps nothing, so the prefers-color-scheme rule applies.
    await userEvent.click(screen.getByRole("button", { name: labels.system }));
    expect(document.documentElement).not.toHaveAttribute("data-theme");
  });

  test("records the choice in the cookie the server reads on the next request", async () => {
    const written = vi.spyOn(document, "cookie", "set");
    stamped("light");
    render(<ThemeToggle current="light" labels={labels} />);

    await userEvent.click(screen.getByRole("button", { name: labels.dark }));

    expect(written).toHaveBeenCalledTimes(1);
    const [pair, ...attributes] = written.mock.calls[0][0].split(";").map((part) => part.trim());
    expect(pair).toBe(`${THEME_COOKIE}=dark`);
    expect(attributes).toEqual(expect.arrayContaining(["path=/", "max-age=31536000", "samesite=lax"]));
  });

  test("keeps every toggle on the page in step", async () => {
    render(
      <>
        <ThemeToggle current="system" labels={labels} />
        <ThemeToggle current="system" labels={labels} />
      </>,
    );

    await userEvent.click(screen.getAllByRole("button", { name: labels.light })[0]);

    expect(screen.getAllByRole("button", { name: labels.dark })).toHaveLength(2);
  });

  test("renders on the server from the theme the server read", () => {
    // The page says light here, so only the server snapshot can produce this.
    stamped("light");
    const html = renderToString(<ThemeToggle current="dark" labels={labels} />);
    expect(html).toContain(`aria-label="${labels.system}"`);
  });

  test("follows the page once in the browser", () => {
    stamped("dark");
    render(<ThemeToggle current="dark" labels={labels} />);
    expect(screen.getByRole("button", { name: labels.system })).toBeInTheDocument();
  });
});
