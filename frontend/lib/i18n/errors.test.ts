import { describe, expect, test } from "vitest";

import { ApiError } from "@/lib/api/client";

import { messageForError } from "./errors";

describe("messageForError", () => {
  test("renders a code the dictionary knows in the interface language", () => {
    const failure = new ApiError("enrollment_closed", 409, "Enrollment is closed");

    expect(messageForError(failure)).toBe("Запись на эту олимпиаду закрыта.");
  });
});

describe("messageForError, unknown codes", () => {
  test("falls back without leaking the server's English message", () => {
    const failure = new ApiError("some_code_added_later", 500, "Widget exploded");

    const shown = messageForError(failure);

    expect(shown).toBe("Что-то пошло не так. Попробуйте ещё раз.");
    expect(shown).not.toContain("Widget exploded");
  });
});
