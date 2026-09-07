import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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
    progression: "free",
    scoring: "points",
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

// Finding 1: progression, scoring and the penalty they interact with used to
// be configurable only by hand-crafted API calls — the settings panel offered
// question_mode and timing, but neither of the contest-level settings. These
// prove the two are now on the same screen as the shape settings they belong
// beside, and that the publish gate's own sequential-progression refusal is
// said here rather than only discovered at publish time.
describe("ContestPanel, question order and scoring", () => {
  test("offers both settings, defaulted to what the contest already holds", () => {
    render(<ContestPanel contest={contest({ progression: "sequential", scoring: "winner" })} editable shapeOpen dict={dict} />);

    expect(
      screen.getByRole("radio", { name: dict.workspace.progression.sequential }),
    ).toBeChecked();
    expect(screen.getByRole("radio", { name: dict.workspace.scoring.winner })).toBeChecked();
  });

  test("warns about the publish gate's own refusal only once sequential is picked", async () => {
    const user = userEvent.setup();
    render(<ContestPanel contest={contest()} editable shapeOpen dict={dict} />);

    expect(screen.queryByText(dict.workspace.settings.shape.sequentialWarning)).toBeNull();

    await user.click(screen.getByRole("radio", { name: dict.workspace.progression.sequential }));

    expect(screen.getByText(dict.workspace.settings.shape.sequentialWarning)).toBeInTheDocument();
  });

  test("hints that an individual-timing contest with no end date never finishes on its own", async () => {
    const user = userEvent.setup();
    render(<ContestPanel contest={contest({ timing: "fixed" })} editable shapeOpen dict={dict} />);

    expect(screen.queryByText(dict.workspace.settings.schedule.endsAtIndividualHint)).toBeNull();

    await user.click(screen.getByRole("radio", { name: dict.workspace.timing.individual }));

    expect(
      screen.getByText(dict.workspace.settings.schedule.endsAtIndividualHint),
    ).toBeInTheDocument();
  });
});
