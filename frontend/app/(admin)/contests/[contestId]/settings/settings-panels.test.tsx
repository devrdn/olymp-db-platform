import { render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, test } from "vitest";

import type { Contest } from "@/lib/api/contests";
import { wallClockFromInstant } from "@/lib/format/datetime";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { ContestPanel } from "./settings-panels";

let dict: Dictionary;

beforeAll(async () => {
  dict = await getDictionary("en");
});

function contest(overrides: Partial<Contest> = {}): Contest {
  return {
    id: "f767af3b-f135-40d2-a3a6-82d368de1004",
    status: "draft",
    enrollment: "invite_only",
    questionMode: "multi",
    timing: "fixed",
    durationMin: undefined,
    startsAt: "2026-11-08T17:00:00Z",
    endsAt: "2026-11-08T21:30:00Z",
    allowedCidrs: [],
    settings: { queryRateLimitPerMin: 0, gracePeriodMin: 0 },
    languages: [{ code: "en", isDefault: true }],
    translations: { en: { title: "Night in the archive" } },
    createdAt: "2026-08-01T10:00:00Z",
    updatedAt: "2026-08-31T17:00:00Z",
    ...overrides,
  } as Contest;
}

describe("ContestPanel, after the server's copy changes", () => {
  /**
   * The fields are uncontrolled — they carry a defaultValue, which React reads
   * once when the input mounts and ignores afterwards. A save revalidates the
   * page and the panel re-renders with the saved contest, so without a remount
   * the field keeps whatever was in it and quietly disagrees with the server.
   * Base UI notices the same thing and warns about a default that changed
   * after initialisation.
   */
  test("shows the schedule the server now holds", () => {
    const { rerender } = render(
      <ContestPanel contest={contest()} editable shapeOpen dict={dict} />,
    );

    expect(screen.getByLabelText(dict.workspace.settings.schedule.endsAt)).toHaveValue(
      wallClockFromInstant("2026-11-08T21:30:00Z"),
    );

    rerender(
      <ContestPanel
        contest={contest({ endsAt: "2026-11-09T01:15:00Z", updatedAt: "2026-08-31T18:00:00Z" })}
        editable
        shapeOpen
        dict={dict}
      />,
    );

    expect(screen.getByLabelText(dict.workspace.settings.schedule.endsAt)).toHaveValue(
      wallClockFromInstant("2026-11-09T01:15:00Z"),
    );
  });

  test("leaves the fields alone when the server's copy did not change", () => {
    // A re-render that is not a save — a theme switch, a parent's state — must
    // not throw away what somebody is in the middle of typing.
    const { rerender } = render(
      <ContestPanel contest={contest()} editable shapeOpen dict={dict} />,
    );

    const field = screen.getByLabelText<HTMLInputElement>(dict.workspace.settings.schedule.endsAt);
    field.value = "2026-11-09T05:00";

    rerender(<ContestPanel contest={contest()} editable shapeOpen dict={dict} />);

    expect(screen.getByLabelText(dict.workspace.settings.schedule.endsAt)).toHaveValue(
      "2026-11-09T05:00",
    );
  });
});
