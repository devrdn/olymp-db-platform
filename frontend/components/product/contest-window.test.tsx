import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { ContestWindow } from "./contest-window";

const props = { locale: "en" as const, unscheduled: "not scheduled", until: "to" };

/** 10:00 and 13:00 in Chisinau, which is what the formatter pins to. */
const MORNING = "2026-05-14T07:00:00Z";
const AFTERNOON = "2026-05-14T10:00:00Z";
const NEXT_DAY = "2026-05-15T10:00:00Z";

describe("ContestWindow", () => {
  test("prints one day and a time range when a contest begins and ends on it", () => {
    const { container } = render(<ContestWindow startsAt={MORNING} endsAt={AFTERNOON} {...props} />);

    // The meridiem is gone from every locale, English included. It was the
    // reason this second line used to break — "01:00 PM" is wider than the
    // column expected — and it left one more hydration trap behind it: the
    // space before AM/PM is U+202F in some ICU versions and an ordinary space
    // in others (lib/format/datetime.ts).
    expect(container).toHaveTextContent("10:00");
    expect(container).toHaveTextContent("13:00");
    expect(container).not.toHaveTextContent(/[AP]M/);
    // The date belongs to the first line only; printing it twice is what made
    // the column overflow and break between the hour and the meridiem.
    expect(container.textContent?.match(/May/g) ?? []).toHaveLength(1);
  });

  /**
   * The separator is a thin space, an en dash and a thin space. It has to be
   * assembled in an expression: JSX text is literal, so a unicode escape typed
   * between the two times is not an escape there and reaches the screen as the
   * six characters ` `. That is what the author's register was printing,
   * and no screenshot review caught it because it looks like punctuation.
   */
  test("separates the two times with real characters, not with an escape", () => {
    const { container } = render(<ContestWindow startsAt={MORNING} endsAt={AFTERNOON} {...props} />);

    expect(container.textContent).not.toContain("\\u");
    expect(container.textContent).toContain(" – ");
  });

  test("names both moments when a contest spans two days", () => {
    const { container } = render(<ContestWindow startsAt={MORNING} endsAt={NEXT_DAY} {...props} />);

    expect(container).toHaveTextContent("to");
    expect(container.textContent?.match(/May/g) ?? []).toHaveLength(2);
  });

  test("says so plainly when no date has been set", () => {
    render(<ContestWindow {...props} />);

    expect(screen.getByText("not scheduled")).toBeInTheDocument();
  });

  test("prints the start alone when there is no end", () => {
    const { container } = render(<ContestWindow startsAt={MORNING} {...props} />);

    expect(container).not.toHaveTextContent("to");
    expect(container).toHaveTextContent("10:00");
  });
});
