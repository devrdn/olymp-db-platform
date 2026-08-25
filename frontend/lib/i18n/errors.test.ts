import { describe, expect, test } from "vitest";

import { ApiError } from "@/lib/api/client";

import { getDictionary } from "./dictionary";
import { messageForError } from "./errors";

describe("messageForError", () => {
  test("renders a known code in the locale asked for", async () => {
    const dict = await getDictionary("ro");
    const failure = new ApiError("enrollment_closed", 409, "Enrollment is closed");

    expect(messageForError(failure, dict)).toBe("Înscrierea la această olimpiadă este închisă.");
  });

  test("falls back without leaking the server's English message", async () => {
    const dict = await getDictionary("ru");
    const failure = new ApiError("some_code_added_later", 500, "Widget exploded");

    const shown = messageForError(failure, dict);

    expect(shown).toBe("Что-то пошло не так. Попробуйте ещё раз.");
    expect(shown).not.toContain("Widget exploded");
  });
});
