import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { Numbers } from "./numbers";

let en: Dictionary;

beforeAll(async () => {
  en = await getDictionary("en");
});

describe("the four numbers", () => {
  /** A strip of zeroes would be a false claim, so a failed read hides it. */
  test("a failed read costs the strip, not the page", () => {
    const { container } = render(<Numbers stats={null} dict={en} />);
    expect(container).toBeEmptyDOMElement();
  });

  test("shows what the installation has done, in mono figures", () => {
    render(
      <Numbers
        stats={{ contests: 12, participants: 340, queries: 91_244, solved: 1_108 }}
        dict={en}
      />,
    );
    expect(screen.getByText("91244")).toHaveClass(/tabular-nums/);
  });

  /** Each of the four captions appears once. */
  test("names all four, each of them once", () => {
    render(
      <Numbers
        stats={{ contests: 12, participants: 340, queries: 91_244, solved: 1_108 }}
        dict={en}
      />,
    );

    const t = en.home.numbers;
    expect(screen.getAllByRole("term").map((term) => term.textContent)).toEqual([
      t.contests,
      t.participants,
      t.queries,
      t.solved,
    ]);
    expect(screen.getAllByRole("definition").map((value) => value.textContent)).toEqual([
      "12",
      "340",
      "91244",
      "1108",
    ]);
  });
});
