import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { Hero } from "./hero";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

/**
 * The hero is the same page for everybody; one thing moves with the visitor.
 *
 * Sending somebody who is already signed in back to the sign-in form is
 * offering them a door they are standing behind, so the main action becomes
 * the way into their own work instead.
 */
describe("the hero", () => {
  test("offers the way in to a visitor, and the way to work to an account", () => {
    const { rerender } = render(<Hero name="Olymp Database System" signedIn={false} dict={en} />);
    expect(screen.getByRole("link", { name: en.home.hero.signIn })).toHaveAttribute("href", "/login");

    rerender(<Hero name="Olymp Database System" signedIn dict={en} />);
    expect(screen.getByRole("link", { name: en.home.hero.mine })).toHaveAttribute("href", "/my");
  });

  /**
   * The catalogue at `/open` lives behind sign-in, so pointing a visitor
   * without a session at it would hand them the sign-in form where they asked
   * for a list of contests. The contests are on this page instead.
   */
  test("sends a visitor to the contests on this page, not to a catalogue behind sign-in", () => {
    render(<Hero name="X" signedIn={false} dict={en} />);
    expect(screen.getByRole("link", { name: en.home.hero.browse })).toHaveAttribute("href", "#contests");
  });

  test("sends an account that may open the catalogue straight to it", () => {
    render(<Hero name="X" signedIn dict={en} />);
    expect(screen.getByRole("link", { name: en.home.hero.browse })).toHaveAttribute("href", "/open");
  });

  test("carries the installation's own name, whatever it is", () => {
    render(<Hero name="Universitatea Tehnică" signedIn={false} dict={en} />);
    expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("Universitatea Tehnică");
  });
});
