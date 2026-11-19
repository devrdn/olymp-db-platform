import { render } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";

vi.mock("@/lib/i18n/server", () => ({
  activeDictionary: async () => en,
  activeLocale: async () => "en",
}));

const screens = import.meta.glob<{ default: () => Promise<React.ReactElement> }>("/app/**/loading.tsx", {
  eager: true,
});

/** A skeleton, told from the other boxes by its own fill. */
const SKELETON = ".bg-line-2.overflow-hidden";

describe("a loading screen", () => {
  test("is found by the glob", () => {
    expect(Object.keys(screens).length).toBeGreaterThan(0);
  });

  // A skeleton's width is a floor for the flex column holding it, even with
  // `max-w-full`: without `min-w-0` the row grows past the screen. Visible
  // only without script, where the skeleton is all the page shows.
  test.each(Object.entries(screens))("%s lets a growing column shrink past its skeletons' widths", async (_, screen) => {
    const { container } = render(await screen.default());

    const growing = [...container.querySelectorAll<HTMLElement>(".flex-1")].filter((column) => {
      const row = column.parentElement;
      return row?.classList.contains("flex") && !row.classList.contains("flex-col") && column.querySelector(SKELETON);
    });
    for (const column of growing) expect(column).toHaveClass("min-w-0");
  });
});
