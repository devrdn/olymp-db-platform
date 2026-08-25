import { describe, expect, test } from "vitest";

import { matchLocale } from "./locale";

describe("matchLocale", () => {
  test("answers in the fallback when no preference is available", () => {
    expect(matchLocale(["de", "fr"], ["en", "ro", "ru"], "en")).toBe("en");
  });
});

describe("matchLocale, regions", () => {
  test("accepts the base language when a region was asked for", () => {
    expect(matchLocale(["ro-MD"], ["en", "ro"], "en")).toBe("ro");
  });

  test("accepts a regional variant when the base language was asked for", () => {
    expect(matchLocale(["ro"], ["en", "ro-MD"], "en")).toBe("ro-MD");
  });

  test("prefers an exact match over the order of the list", () => {
    expect(matchLocale(["ru"], ["en", "ro", "ru"], "en")).toBe("ru");
  });
});
