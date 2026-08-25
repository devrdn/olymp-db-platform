import { describe, expect, test } from "vitest";

import { LOCALES } from "./config";
import { readLocale } from "./locale";

describe("readLocale", () => {
  test("answers with what the visitor chose", () => {
    expect(readLocale("ro")).toBe("ro");
  });

  test("answers English when nothing has been chosen", () => {
    expect(readLocale(undefined)).toBe("en");
  });

  test("answers English rather than trusting a value we do not speak", () => {
    expect(readLocale("de")).toBe("en");
    expect(readLocale("../../etc/passwd")).toBe("en");
  });

  test("speaks exactly the languages the app declares", () => {
    for (const locale of LOCALES) expect(readLocale(locale)).toBe(locale);
  });
});
