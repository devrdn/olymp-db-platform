import { describe, expect, test } from "vitest";

import { ApiError } from "@/lib/api/client";

import { getDictionary } from "./dictionary";
import { knownMessage, messageForCode } from "./errors";

describe("messageForCode", () => {
  test("renders a known code in the locale asked for", async () => {
    const dict = await getDictionary("ro");
    const failure = new ApiError("enrollment_closed", 409, "Enrollment is closed");

    expect(messageForCode(failure.code, dict.errors)).toBe("Înscrierea la această olimpiadă este închisă.");
  });

  test("falls back without leaking the server's English message", async () => {
    const dict = await getDictionary("ru");
    const failure = new ApiError("some_code_added_later", 500, "Widget exploded");

    const shown = messageForCode(failure.code, dict.errors);

    expect(shown).toBe("Что-то пошло не так. Попробуйте ещё раз.");
    expect(shown).not.toContain("Widget exploded");
  });

  // A code is the API's text, so it can name anything an object inherits.
  test.each(["constructor", "toString", "__proto__", "hasOwnProperty"])(
    "gives the fallback for %s, which the dictionary only inherits",
    async (code) => {
      const dict = await getDictionary("en");

      expect(messageForCode(code, dict.errors)).toBe(dict.errors.fallback);
    },
  );
});

describe("knownMessage", () => {
  test("is the dictionary's sentence for a code it has", async () => {
    const dict = await getDictionary("ru");

    expect(knownMessage("enrollment_closed", dict.errors)).toBe(dict.errors.enrollment_closed);
  });

  test("is nothing for a code the dictionary lacks or only inherits", async () => {
    const dict = await getDictionary("en");

    expect(knownMessage("some_code_added_later", dict.errors)).toBeUndefined();
    expect(knownMessage("constructor", dict.errors)).toBeUndefined();
  });
});
