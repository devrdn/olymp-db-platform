import { describe, expect, test } from "vitest";

import { ApiError } from "@/lib/api/client";

import { getDictionary } from "./dictionary";
import { messageForCode } from "./errors";

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
});
