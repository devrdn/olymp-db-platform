import { describe, expect, test } from "vitest";

import { plural } from "./plural";

const minutes = { one: "{n} минуту", few: "{n} минуты", many: "{n} минут", other: "{n} минуты" };

describe("plural", () => {
  test("picks the form the locale's plural rules name for the number", () => {
    expect(plural(minutes, 1, "ru")).toBe("1 минуту");
    expect(plural(minutes, 3, "ru")).toBe("3 минуты");
    expect(plural(minutes, 20, "ru")).toBe("20 минут");
    expect(plural(minutes, 21, "ru")).toBe("21 минуту");
  });

  test("reads English and Romanian with their own rules", () => {
    const en = { one: "{n} minute", few: "{n} minutes", many: "{n} minutes", other: "{n} minutes" };
    const ro = { one: "{n} minut", few: "{n} minute", many: "{n} de minute", other: "{n} de minute" };

    expect(plural(en, 1, "en")).toBe("1 minute");
    expect(plural(en, 2, "en")).toBe("2 minutes");
    expect(plural(ro, 1, "ro")).toBe("1 minut");
    expect(plural(ro, 2, "ro")).toBe("2 minute");
    expect(plural(ro, 20, "ro")).toBe("20 de minute");
  });
});
