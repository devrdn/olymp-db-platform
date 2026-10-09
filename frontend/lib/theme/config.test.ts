import { describe, expect, test } from "vitest";

import { DEFAULT_THEME, nextTheme, readTheme, themeAttribute, THEMES } from "./config";

describe("readTheme", () => {
  test("accepts each theme the system declares", () => {
    for (const theme of THEMES) {
      expect(readTheme(theme)).toBe(theme);
    }
  });

  test("treats a value that is not a theme as absent rather than trusting it", () => {
    // The cookie is not httpOnly, so anything can be in it.
    expect(readTheme("../../etc/passwd")).toBe(DEFAULT_THEME);
    expect(readTheme("")).toBe(DEFAULT_THEME);
    expect(readTheme(undefined)).toBe(DEFAULT_THEME);
    expect(readTheme(null)).toBe(DEFAULT_THEME);
  });
});

describe("themeAttribute", () => {
  test("stamps nothing for the system theme", () => {
    expect(themeAttribute("system")).toBeUndefined();
  });

  test("stamps an explicit choice so it wins in both directions", () => {
    expect(themeAttribute("light")).toBe("light");
    expect(themeAttribute("dark")).toBe("dark");
  });
});

describe("nextTheme", () => {
  test("walks every theme and returns to the start", () => {
    const walked = THEMES.map(() => 0).reduce<string[]>(
      (seen) => [...seen, (nextTheme(seen.at(-1) as never) ?? "") as string],
      [DEFAULT_THEME],
    );

    expect(walked).toEqual(["system", "light", "dark", "system"]);
  });

  test("returns to a known theme from a value that is not one", () => {
    expect(THEMES).toContain(nextTheme("chartreuse" as never));
  });
});
