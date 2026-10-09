import { render, screen } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import { ContestWindow } from "./contest-window";

const props = { locale: "en" as const, unscheduled: "not scheduled", until: "to" };

/** 10:00 and 13:00 in Chisinau, the zone the formatter pins. */
const MORNING = "2026-05-14T07:00:00Z";
const AFTERNOON = "2026-05-14T10:00:00Z";
const NEXT_DAY = "2026-05-15T10:00:00Z";

describe("ContestWindow", () => {
  test("prints one day and a time range when a contest begins and ends on it", () => {
    const { container } = render(<ContestWindow startsAt={MORNING} endsAt={AFTERNOON} {...props} />);

    // No meridiem in any locale: the space before AM/PM differs between ICU
    // versions (U+202F or a plain space), a hydration trap.
    expect(container).toHaveTextContent("10:00");
    expect(container).toHaveTextContent("13:00");
    expect(container).not.toHaveTextContent(/[AP]M/);
    // The date appears once, on the first line.
    expect(container.textContent?.match(/May/g) ?? []).toHaveLength(1);
  });

  /** A unicode escape typed as JSX text reaches the screen literally. */
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
