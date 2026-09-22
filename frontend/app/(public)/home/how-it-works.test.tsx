import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { HowItWorks } from "./how-it-works";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

describe("the three points", () => {
  /**
   * Three sentences laid out by the same loop is where one of them quietly
   * becomes a second copy of another, and a reader has no way to know that
   * the promise they are not being told about was ever meant to be there.
   */
  test("says all three things, each of them once", () => {
    render(<HowItWorks dict={en} />);

    const items = screen.getAllByRole("listitem").map((item) => item.textContent);
    expect(items).toEqual([en.home.how.live, en.home.how.checked, en.home.how.own]);
  });

  test("carries a heading of its own, so the section can be named", () => {
    render(<HowItWorks dict={en} />);
    expect(screen.getByRole("heading", { level: 2 })).toHaveTextContent(en.home.how.heading);
  });
});
