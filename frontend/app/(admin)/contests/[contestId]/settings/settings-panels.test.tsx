import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test } from "vitest";

import type { Contest } from "@/lib/api/contests";
import { wallClockFromInstant } from "@/lib/format/datetime";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { ContestPanel, PrizePanel } from "./settings-panels";

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
    icpcPenaltyMin: 20,
    resultDownload: true,
    schemaDiagram: true,
    prizePlaces: { first: 0, second: 0, third: 0 },
    timing: "fixed",
    durationMin: undefined,
    startsAt: "2026-11-08T17:00:00Z",
    endsAt: "2026-11-08T21:30:00Z",
    allowedCidrs: [],
    settings: { queryRateLimitPerMin: 0, gracePeriodMin: 0 },
    leaderboard: { freezeMin: null, names: "login", revealedAt: undefined },
    languages: [{ code: "en", isDefault: true }],
    translations: { en: { title: "Night in the archive" } },
    createdAt: "2026-08-01T10:00:00Z",
    updatedAt: "2026-08-31T17:00:00Z",
    ...overrides,
  } as Contest;
}

describe("ContestPanel, after the server's copy changes", () => {
  /**
   * Uncontrolled fields read `defaultValue` once, so a save must remount them
   * to show the server's values.
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
    // A re-render that is not a save must keep what is being typed.
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

// Progression and scoring sit beside the shape settings, and the publish gate's
// sequential-progression refusal is stated here.
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

  test("says an attempt limit is optional once ICPC is picked with the order sequential", async () => {
    const user = userEvent.setup();
    render(<ContestPanel contest={contest({ progression: "sequential" })} editable shapeOpen dict={dict} />);

    expect(screen.getByText(dict.workspace.settings.shape.sequentialWarning)).toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: dict.workspace.scoring.icpc }));

    expect(screen.queryByText(dict.workspace.settings.shape.sequentialWarning)).toBeNull();
    expect(screen.getByText(dict.workspace.settings.shape.sequentialIcpcNote)).toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: dict.workspace.scoring.winner }));

    expect(screen.getByText(dict.workspace.settings.shape.sequentialWarning)).toBeInTheDocument();
    expect(screen.queryByText(dict.workspace.settings.shape.sequentialIcpcNote)).toBeNull();
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

/** Rules stay on screen; explanations move behind a "?". */
describe("ContestPanel, rules on screen and explanations behind a question mark", () => {
  test("keeps the network and rate rules visible, and the access explanation closed", () => {
    render(<ContestPanel contest={contest()} editable shapeOpen dict={dict} />);

    const t = dict.workspace.settings.access;
    expect(screen.getByText(t.networkHint)).toBeVisible();
    expect(screen.getByText(t.rateHint)).toBeVisible();
    expect(screen.getByText(t.help)).not.toBeVisible();
  });

  test("the rules remain the fields' own descriptions", () => {
    render(<ContestPanel contest={contest()} editable shapeOpen dict={dict} />);

    const t = dict.workspace.settings.access;
    expect(screen.getByLabelText(t.network)).toHaveAccessibleDescription(t.networkHint);
    expect(screen.getByLabelText(t.rate)).toHaveAccessibleDescription(t.rateHint);
  });

  test("opens an explanation from its question mark", async () => {
    const user = userEvent.setup();
    render(<ContestPanel contest={contest()} editable shapeOpen dict={dict} />);

    const t = dict.workspace.settings.shape;
    const hint = screen
      .getAllByRole("button", { name: dict.chrome.helpLabel })
      .find((button) => button.getAttribute("aria-describedby") === screen.getByText(t.orderHelp).id)!;

    await user.click(hint);
    expect(screen.getByText(t.orderHelp)).toBeVisible();
  });

  /**
   * A disabled fieldset spares only buttons in its legend, so the "?" lives
   * there and still works when frozen; the group name stays free of "Hint".
   */
  test("still explains the question order once the shape is frozen", async () => {
    const user = userEvent.setup();
    render(<ContestPanel contest={contest()} editable shapeOpen={false} dict={dict} />);

    const t = dict.workspace.settings.shape;
    const group = screen.getByRole("group", { name: t.order });
    expect(group).toBeDisabled();

    const hint = screen
      .getAllByRole("button", { name: dict.chrome.helpLabel })
      .find((button) => button.getAttribute("aria-describedby") === screen.getByText(t.orderHelp).id)!;
    expect(hint).toBeEnabled();

    await user.click(hint);
    expect(screen.getByText(t.orderHelp)).toBeVisible();
  });
});

describe("ContestPanel, the result download", () => {
  test("offers the box ticked as the contest holds it, with its rule as its description", () => {
    const t = dict.workspace.settings.access;
    const { unmount } = render(<ContestPanel contest={contest()} editable shapeOpen dict={dict} />);

    const box = screen.getByRole("checkbox", { name: t.resultDownload });
    expect(box).toBeChecked();
    expect(box).toHaveAccessibleDescription(t.resultDownloadHint);
    unmount();

    render(<ContestPanel contest={contest({ resultDownload: false })} editable shapeOpen dict={dict} />);
    expect(screen.getByRole("checkbox", { name: t.resultDownload })).not.toBeChecked();
  });

  test("submits the box with the rest of the contest's fields", async () => {
    const user = userEvent.setup();
    render(<ContestPanel contest={contest()} editable shapeOpen dict={dict} />);

    const box = screen.getByRole("checkbox", { name: dict.workspace.settings.access.resultDownload });
    const form = box.closest("form")!;
    expect(new FormData(form).get("resultDownload")).toBe("on");

    await user.click(box);
    expect(new FormData(form).has("resultDownload")).toBe(false);
  });

  test("stays open while the contest runs, unlike the shape", () => {
    render(
      <ContestPanel contest={contest({ status: "running" })} editable shapeOpen={false} dict={dict} />,
    );

    expect(screen.getByRole("checkbox", { name: dict.workspace.settings.access.resultDownload })).toBeEnabled();
  });

  test("closes with the other settings once the contest is over", () => {
    render(
      <ContestPanel contest={contest({ status: "finished" })} editable={false} shapeOpen={false} dict={dict} />,
    );

    expect(screen.getByRole("checkbox", { name: dict.workspace.settings.access.resultDownload })).toBeDisabled();
  });
});

describe.each(["en", "ro", "ru"] as const)("the result download's rule in %s", (locale) => {
  // The hint must carry the three facts an organiser needs: the default, when
  // a change reaches a page already open, and that copying the table stays.
  const words = {
    en: { on: /default/i, reload: /reload/i, copy: /cop(y|ied)/i },
    ro: { on: /implicit/i, reload: /reîncărc/i, copy: /cop/i },
    ru: { on: /умолчани/i, reload: /обнов/i, copy: /копир/i },
  }[locale];

  test("says it is on by default, applies at the next reload, and does not stop copying", async () => {
    const hint = (await getDictionary(locale)).workspace.settings.access.resultDownloadHint;

    expect(hint).toMatch(words.on);
    expect(hint).toMatch(words.reload);
    expect(hint).toMatch(words.copy);
  });
});

describe("ContestPanel, the schema diagram", () => {
  test("offers the box ticked as the contest holds it, with its rule as its description", () => {
    const t = dict.workspace.settings.access;
    const { unmount } = render(<ContestPanel contest={contest()} editable shapeOpen dict={dict} />);

    const box = screen.getByRole("checkbox", { name: t.schemaDiagram });
    expect(box).toBeChecked();
    expect(box).toHaveAccessibleDescription(t.schemaDiagramHint);
    unmount();

    render(<ContestPanel contest={contest({ schemaDiagram: false })} editable shapeOpen dict={dict} />);
    expect(screen.getByRole("checkbox", { name: t.schemaDiagram })).not.toBeChecked();
  });

  test("submits the box with the rest of the contest's fields", async () => {
    const user = userEvent.setup();
    render(<ContestPanel contest={contest()} editable shapeOpen dict={dict} />);

    const box = screen.getByRole("checkbox", { name: dict.workspace.settings.access.schemaDiagram });
    const form = box.closest("form")!;
    expect(new FormData(form).get("schemaDiagram")).toBe("on");

    await user.click(box);
    expect(new FormData(form).has("schemaDiagram")).toBe(false);
  });

  test("stays open while the contest runs, unlike the shape", () => {
    render(
      <ContestPanel contest={contest({ status: "running" })} editable shapeOpen={false} dict={dict} />,
    );

    expect(screen.getByRole("checkbox", { name: dict.workspace.settings.access.schemaDiagram })).toBeEnabled();
  });

  test("closes with the other settings once the contest is over", () => {
    render(
      <ContestPanel contest={contest({ status: "finished" })} editable={false} shapeOpen={false} dict={dict} />,
    );

    expect(screen.getByRole("checkbox", { name: dict.workspace.settings.access.schemaDiagram })).toBeDisabled();
  });
});

describe.each(["en", "ro", "ru"] as const)("the schema diagram's rule in %s", (locale) => {
  // The hint must carry what an organiser needs: the default, when a change
  // reaches a page already open, and that the foreign keys stay listed.
  const words = {
    en: { on: /default/i, reload: /reload/i, keys: /foreign keys/i },
    ro: { on: /implicit/i, reload: /reîncărc/i, keys: /cheile străine/i },
    ru: { on: /умолчани/i, reload: /обнов/i, keys: /внешние ключи/i },
  }[locale];

  test("says it is on by default, applies at the next reload, and keeps the foreign keys listed", async () => {
    const hint = (await getDictionary(locale)).workspace.settings.access.schemaDiagramHint;

    expect(hint).toMatch(words.on);
    expect(hint).toMatch(words.reload);
    expect(hint).toMatch(words.keys);
  });
});

// ICPC penalty (docs/ARCHITECTURE.md §6.1.1): minutes 0..240, shown only for
// ICPC and locked with the shape.
describe("ContestPanel, the ICPC penalty", () => {
  test("is absent while another scoring mode is picked", () => {
    render(<ContestPanel contest={contest()} editable shapeOpen dict={dict} />);

    expect(
      screen.queryByLabelText(dict.workspace.settings.shape.icpcPenalty),
    ).not.toBeInTheDocument();
  });

  test("appears, defaulted to the contest's own value, once ICPC is picked", async () => {
    const user = userEvent.setup();
    render(<ContestPanel contest={contest({ icpcPenaltyMin: 45 })} editable shapeOpen dict={dict} />);

    await user.click(screen.getByRole("radio", { name: dict.workspace.scoring.icpc }));

    expect(screen.getByLabelText(dict.workspace.settings.shape.icpcPenalty)).toHaveValue(45);
  });

  test("shows the penalty already configured on an ICPC contest", () => {
    render(
      <ContestPanel contest={contest({ scoring: "icpc", icpcPenaltyMin: 30 })} editable shapeOpen dict={dict} />,
    );

    expect(screen.getByLabelText(dict.workspace.settings.shape.icpcPenalty)).toHaveValue(30);
  });

  test("locks the penalty once the shape is frozen, same as the scoring radio", () => {
    render(
      <ContestPanel
        contest={contest({ status: "running", scoring: "icpc", icpcPenaltyMin: 30 })}
        editable
        shapeOpen={false}
        dict={dict}
      />,
    );

    expect(screen.getByRole("radio", { name: dict.workspace.scoring.icpc })).toBeDisabled();
    expect(screen.getByLabelText(dict.workspace.settings.shape.icpcPenalty)).toBeDisabled();
  });
});

describe("ContestPanel, the leaderboard", () => {
  test("asks for an amount only once a freeze is chosen", async () => {
    const user = userEvent.setup();
    render(<ContestPanel contest={contest()} editable shapeOpen dict={dict} />);
    const t = dict.workspace.settings.leaderboard;

    expect(screen.getByLabelText(t.freezeNone)).toBeChecked();
    expect(screen.queryByLabelText(t.freezeAmount)).not.toBeInTheDocument();

    await user.click(screen.getByLabelText(t.freezeBefore));
    expect(screen.getByLabelText(t.freezeAmount)).toHaveValue(30);
  });

  test("shows a freeze of whole hours in hours", () => {
    render(
      <ContestPanel
        contest={contest({ leaderboard: { freezeMin: 120, names: "full_name", revealedAt: undefined } })}
        editable
        shapeOpen
        dict={dict}
      />,
    );
    const t = dict.workspace.settings.leaderboard;

    expect(screen.getByLabelText(t.freezeAmount)).toHaveValue(2);
    expect(screen.getByLabelText(t.unitHours)).toBeChecked();
    expect(screen.getByLabelText(t.namesFullName)).toBeChecked();
    expect(screen.getByText(t.namesPublicHint)).toBeInTheDocument();
  });

  test("locks the freeze once the contest runs, and leaves the label free", () => {
    render(
      <ContestPanel
        contest={contest({ status: "running", leaderboard: { freezeMin: 30, names: "login", revealedAt: undefined } })}
        editable
        shapeOpen={false}
        dict={dict}
      />,
    );
    const t = dict.workspace.settings.leaderboard;

    expect(screen.getByLabelText(t.freezeBefore)).toBeDisabled();
    expect(screen.getByLabelText(t.freezeAmount)).toBeDisabled();
    expect(screen.getByLabelText(t.namesFullName)).toBeEnabled();
  });
});

/**
 * The words for two installation-backed fields, checked against what the
 * service does: grace_period_min keeps the participants' databases after the
 * finish (late answers are DEADLINE_GRACE), and a zero rate falls back to
 * QUERY_PER_MINUTE, which also caps a higher one. Enrolment under individual
 * timing stays open while the contest runs.
 */
describe("ContestPanel, the fields say what the service does with them", () => {
  const meaning = {
    en: {
      database: /database/i,
      installation: /installation/i,
      oldGrace: /grace|late answer/i,
      noLimit: /no limit/i,
      individual: /individual timing/i,
    },
    ro: {
      database: /baz/i,
      installation: /instalări/i,
      oldGrace: /grație|întârziat/i,
      noLimit: /fără limită/i,
      individual: /timp propriu/i,
    },
    ru: {
      database: /баз/i,
      installation: /установк/i,
      oldGrace: /грейс|опоздавший/i,
      noLimit: /без ограничения/i,
      individual: /собственном времени/i,
    },
  } as const;

  describe.each(Object.keys(meaning) as (keyof typeof meaning)[])("in %s", (locale) => {
    let local: Dictionary;
    const words = meaning[locale];

    beforeAll(async () => {
      local = await getDictionary(locale);
    });

    test("names the database grace by what it keeps, not by late answers", () => {
      render(<ContestPanel contest={contest()} editable shapeOpen dict={local} />);
      const t = local.workspace.settings.schedule;

      const field = screen.getByLabelText(t.grace);
      expect(field).toHaveAccessibleDescription(t.graceHelp);
      expect(t.grace).toMatch(words.database);
      expect(t.grace).not.toMatch(words.oldGrace);
      expect(t.graceHelp).toMatch(words.database);
      expect(t.graceHelp).toMatch(words.installation);
      expect(t.graceHelp).toMatch(/\b0\b/);
    });

    test("says a zero rate is the installation's limit, never no limit", () => {
      render(<ContestPanel contest={contest()} editable shapeOpen dict={local} />);
      const t = local.workspace.settings.access;

      expect(screen.getByLabelText(t.rate)).toHaveAccessibleDescription(t.rateHint);
      expect(t.rateHint).toMatch(words.installation);
      expect(t.rateHint).toMatch(/\b0\b/);
      expect(t.rateHint).not.toMatch(words.noLimit);
      expect(t.rateHint).not.toMatch(/\b30\b/);
    });

    test("says enrolment under individual timing stays open while the contest runs", () => {
      render(<ContestPanel contest={contest()} editable shapeOpen dict={local} />);
      const t = local.workspace.settings.schedule;

      expect(screen.getByLabelText(t.enrollmentDeadline)).toHaveAccessibleDescription(
        t.enrollmentDeadlineHint,
      );
      expect(t.enrollmentDeadlineHint).toMatch(words.individual);
    });
  });

  test("in English, the grace gives no extra time and a higher rate has no effect", () => {
    const t = dict.workspace.settings;

    expect(t.schedule.graceHelp).toMatch(/after the contest finishes/i);
    expect(t.schedule.graceHelp).toMatch(/no extra time/i);
    expect(t.access.rateHint).toMatch(/above .* no effect/i);
  });
});

describe("PrizePanel", () => {
  const t = () => dict.workspace.settings.prizes;

  test("offers the three counts as the contest holds them, bounded and described by their rule", () => {
    render(<PrizePanel contest={contest({ prizePlaces: { first: 1, second: 2, third: 3 } })} editable dict={dict} />);

    for (const [label, value] of [
      [t().first, 1],
      [t().second, 2],
      [t().third, 3],
    ] as const) {
      const field = screen.getByRole("spinbutton", { name: label });
      expect(field).toHaveValue(value);
      expect(field).toHaveAttribute("min", "0");
      expect(field).toHaveAttribute("max", "1000");
      expect(field).toHaveAttribute("step", "1");
      expect(field).toHaveAccessibleDescription(`${t().hint} ${t().winnerHint}`);
    }
  });

  test("defaults to no prizes at all", () => {
    render(<PrizePanel contest={contest()} editable dict={dict} />);

    for (const label of [t().first, t().second, t().third]) {
      expect(screen.getByRole("spinbutton", { name: label })).toHaveValue(0);
    }
  });

  test("submits the counts as typed, from the keyboard alone", async () => {
    const user = userEvent.setup();
    render(<PrizePanel contest={contest({ status: "finished" })} editable dict={dict} />);

    const first = screen.getByRole("spinbutton", { name: t().first });
    await user.click(first);
    await user.clear(first);
    await user.keyboard("2");
    await user.tab();
    expect(screen.getByRole("spinbutton", { name: t().second })).toHaveFocus();
    await user.keyboard("{Backspace}5");

    const form = new FormData(first.closest("form")!);
    expect(form.get("prizeFirst")).toBe("2");
    expect(form.get("prizeSecond")).toBe("5");
    expect(form.get("prizeThird")).toBe("0");
    expect(form.get("contestId")).toBe(contest().id);
    expect(screen.getByRole("button", { name: dict.workspace.settings.save })).toBeEnabled();
  });

  test("closes once the contest is archived, and says so", () => {
    render(<PrizePanel contest={contest({ status: "archived" })} editable={false} dict={dict} />);

    for (const label of [t().first, t().second, t().third]) {
      expect(screen.getByRole("spinbutton", { name: label })).toBeDisabled();
    }
    expect(screen.getByRole("button", { name: dict.workspace.settings.save })).toBeDisabled();
    expect(screen.getByText(dict.workspace.facts.frozen)).toBeInTheDocument();
  });

  // The save leaves updatedAt alone, so the counts alone must remount the form.
  test("shows the counts the server now holds after a save", () => {
    const { rerender } = render(<PrizePanel contest={contest()} editable dict={dict} />);

    rerender(<PrizePanel contest={contest({ prizePlaces: { first: 4, second: 0, third: 0 } })} editable dict={dict} />);

    expect(screen.getByRole("spinbutton", { name: t().first })).toHaveValue(4);
  });

  test("lays the three fields out in one column on a phone", () => {
    render(<PrizePanel contest={contest()} editable dict={dict} />);

    const grid = screen.getByRole("spinbutton", { name: t().first }).closest(".grid") as HTMLElement;
    expect(grid.className).toContain("narrow:grid-cols-3");
    expect(grid.className).not.toMatch(/(^| )grid-cols-3/);
  });
});

describe.each(["en", "ro", "ru"] as const)("the prize places' rule in %s", (locale) => {
  const words = {
    en: { tie: /tie/i, none: /all three at 0/i, unscored: /scored nothing/i, winner: /winner.*first prize/i, archive: /archiv/i },
    ro: { tie: /egalitate/i, none: /toate trei la 0/i, unscored: /n-a punctat/i, winner: /câștigător.*premiul I/i, archive: /arhiv/i },
    ru: { tie: /равн/i, none: /все три равны 0/i, unscored: /ничего не набрал/i, winner: /победител.*первую премию/i, archive: /архив/i },
  }[locale];

  test("says ties share a place, 0 means none, a zero score takes none, the winner holds the first prize, and the archive closes them", async () => {
    const t = (await getDictionary(locale)).workspace.settings.prizes;

    expect(t.hint).toMatch(words.tie);
    expect(t.hint).toMatch(words.none);
    expect(t.hint).toMatch(words.unscored);
    expect(t.winnerHint).toMatch(words.winner);
    expect(t.help).toMatch(words.archive);
  });
});
