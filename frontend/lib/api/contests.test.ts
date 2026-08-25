import { describe, expect, test } from "vitest";

import { contestListSchema } from "./contests";

describe("contestListSchema", () => {
  test("parses the listing the API actually returns", () => {
    const payload = {
      items: [
        {
          id: "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6",
          status: "running",
          enrollment: "open",
          question_mode: "multi",
          lang: "ru",
          title: "Ночь в архиве",
          description: "Опись пропала между полуночью и рассветом.",
          starts_at: "2026-11-08T19:00:00Z",
          ends_at: "2026-11-08T21:00:00Z",
        },
      ],
      total: 1,
    };

    const parsed = contestListSchema.parse(payload);

    expect(parsed.total).toBe(1);
    expect(parsed.items[0]).toMatchObject({
      status: "running",
      questionMode: "multi",
      title: "Ночь в архиве",
    });
  });
});
