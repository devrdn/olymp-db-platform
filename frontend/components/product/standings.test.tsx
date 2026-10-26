import { render, screen, within } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import { getDictionary } from "@/lib/i18n/dictionary";
import {
  identityHue,
  type Standings,
  type StandingsRow,
} from "@/lib/api/leaderboard";
import { formatTime } from "@/lib/format/datetime";

import { PlaceBadge, PrizeBadge, StandingsView } from "./standings";

const t = en.leaderboard;

function row(overrides: Partial<StandingsRow>): StandingsRow {
  return {
    place: 1,
    prize: null,
    label: "ivanov",
    deleted: false,
    points: 10,
    solved: 1,
    penalty: undefined,
    cells: undefined,
    lastScoredAt: "2026-09-20T10:00:00Z",
    winner: false,
    isYou: false,
    ...overrides,
  };
}

function standings(overrides: Partial<Standings> = {}): Standings {
  return {
    state: "live",
    scoring: "points",
    title: "The Library Murder",
    frozenAt: undefined,
    endsAt: "2099-01-01T00:00:00Z",
    generatedAt: "2026-09-20T10:05:00Z",
    truncated: false,
    questions: undefined,
    rows: [
      row({ place: 1, prize: 1, label: "alpha", points: 40 }),
      row({ place: 2, prize: 2, label: "beta", points: 20, isYou: true }),
      row({ place: 3, prize: 3, label: "gamma", points: 10 }),
      row({ place: 4, prize: null, label: "delta", points: 5 }),
    ],
    ...overrides,
  };
}

function show(value: Standings, variant: "panel" | "page" = "panel") {
  return render(
    <StandingsView standings={value} dict={en} locale="en" variant={variant} />,
  );
}

function rowOf(label: string) {
  return screen.getByText(label).closest("tr") as HTMLElement;
}

describe("the standings", () => {
  test("give each prize tier its medal and its numeral, and name the tier in words", () => {
    show(standings());

    for (const [label, medal, numeral, name] of [
      ["alpha", "gold", "I", t.prizes.first],
      ["beta", "silver", "II", t.prizes.second],
      ["gamma", "bronze", "III", t.prizes.third],
    ]) {
      const cell = within(rowOf(label)).getByTestId("prize");
      expect(cell).toHaveAttribute("data-medal", medal);
      expect(within(cell).getByText(numeral)).toHaveAttribute("aria-hidden");
      expect(within(cell).getByText(name)).toHaveClass("sr-only");
    }
  });

  test("show a dash, named as no prize, for a row without one in the page's prize column, whatever its place", () => {
    show(standings(), "page");

    const cell = within(tableRowOf("delta")).getByTestId("prize");
    expect(cell).not.toHaveAttribute("data-medal");
    expect(within(cell).getByText("—")).toHaveAttribute("aria-hidden");
    expect(within(cell).getByText(t.prizes.none)).toHaveClass("sr-only");
    expect(cell).not.toHaveTextContent("4");
  });

  test("show the place first, then the prize, as numbers and words in the row's accessible text", () => {
    show(standings(), "page");

    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent);
    expect(headers.slice(0, 3)).toEqual([t.columns.place, t.columns.prize, t.columns.participant]);
    for (const [label, place] of [
      ["alpha", "1"],
      ["beta", "2"],
      ["gamma", "3"],
      ["delta", "4"],
    ]) {
      const cell = within(tableRowOf(label)).getAllByRole("cell")[0];
      expect(within(cell).getByTestId("place")).toHaveTextContent(place);
      expect(within(cell).getByText(place)).not.toHaveAttribute("aria-hidden");
    }
  });

  test("in the narrow panel keep one place column and draw the medal beside the number", () => {
    show(standings());

    expect(screen.queryByRole("columnheader", { name: t.columns.prize })).not.toBeInTheDocument();
    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent);
    expect(headers.slice(0, 2)).toEqual([t.columns.place, t.columns.participant]);

    const cell = within(rowOf("beta")).getAllByRole("cell")[0];
    expect(within(cell).getByTestId("place")).toHaveTextContent("2");
    expect(within(cell).getByTestId("prize")).toHaveAttribute("data-medal", "silver");
    expect(within(cell).getByText(t.prizes.second)).toHaveClass("sr-only");

    const plain = within(rowOf("delta")).getAllByRole("cell")[0];
    expect(within(plain).getByTestId("place")).toHaveTextContent("4");
    expect(within(plain).queryByTestId("prize")).not.toBeInTheDocument();
  });

  test("in the narrow panel pin the fixed columns to 10 rem with a prize, so the name keeps its room", () => {
    // The default side pane leaves the table about 13.75 rem: place (with the
    // medal in it) 4 + points 6 leaves the name column room; a separate prize
    // column would take it all.
    show(standings());

    const header = (name: string) => screen.getByRole("columnheader", { name });
    expect(header(t.columns.place)).toHaveClass("w-16");
    expect(header(t.columns.points)).toHaveClass("w-24");
    expect(screen.getAllByRole("columnheader")).toHaveLength(3);
  });

  test("in the narrow ICPC panel fold the medal into the place column too", () => {
    show(icpcStandings(), "panel");

    expect(screen.queryByRole("columnheader", { name: t.columns.prize })).not.toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: t.columns.place })).toHaveClass("w-16");
    expect(within(rowOf("alpha")).getAllByRole("cell")[0]).toContainElement(
      within(rowOf("alpha")).getByTestId("prize"),
    );
  });

  test("tied rows share their place and their tier, and the next place skips", () => {
    show(
      standings({
        rows: [
          row({ place: 1, prize: 1, label: "alpha", points: 40 }),
          row({ place: 1, prize: 1, label: "beta", points: 40 }),
          row({ place: 3, prize: 2, label: "gamma", points: 10 }),
        ],
      }),
    );

    expect(within(rowOf("alpha")).getByTestId("place")).toHaveTextContent("1");
    expect(within(rowOf("beta")).getByTestId("place")).toHaveTextContent("1");
    expect(within(rowOf("gamma")).getByTestId("place")).toHaveTextContent("3");
    expect(within(rowOf("alpha")).getByTestId("prize")).toHaveAttribute("data-medal", "gold");
    expect(within(rowOf("beta")).getByTestId("prize")).toHaveAttribute("data-medal", "gold");
    expect(within(rowOf("gamma")).getByTestId("prize")).toHaveAttribute("data-medal", "silver");
  });

  test("show a dash, named as no place, for a row that has not answered, and keep its prize cell empty", () => {
    show(
      standings({
        rows: [
          row({ place: 1, prize: 1, label: "alpha", points: 40 }),
          row({ place: null, prize: null, label: "idle", points: 0, solved: 0 }),
        ],
      }),
      "page",
    );

    const cell = within(tableRowOf("idle")).getByTestId("place");
    expect(within(cell).getByText("—")).toHaveAttribute("aria-hidden");
    expect(within(cell).getByText(t.unplaced)).toHaveClass("sr-only");
    expect(cell).not.toHaveAttribute("data-medal");
    expect(within(tableRowOf("idle")).getByTestId("prize")).toHaveTextContent(t.prizes.none);
  });

  test("keep the rows in the order they were ranked", () => {
    show(
      standings({
        rows: [
          row({ place: 1, prize: null, label: "alpha" }),
          row({ place: 2, prize: 3, label: "beta" }),
          row({ place: 3, prize: null, label: "gamma" }),
        ],
      }),
    );

    const body = screen.getAllByRole("row").slice(1);
    ["alpha", "beta", "gamma"].forEach((label, i) => {
      expect(within(body[i]).getByText(label)).toBeInTheDocument();
    });
  });

  test("give no medal and no prize column when the contest has no prize places", () => {
    show(
      standings({
        rows: [
          row({ place: 1, label: "alpha", points: 40 }),
          row({ place: 2, label: "beta", points: 20 }),
        ],
      }),
      "page",
    );

    expect(screen.queryByRole("columnheader", { name: t.columns.prize })).not.toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: t.columns.place })).toBeInTheDocument();
    expect(screen.queryByTestId("prize")).not.toBeInTheDocument();
    for (const [label, place] of [
      ["alpha", "1"],
      ["beta", "2"],
    ]) {
      const tr = tableRowOf(label);
      expect(within(tr).getByTestId("place")).toHaveTextContent(place);
      expect(tr.className).not.toMatch(/gold|silver|bronze/);
    }
    expect(screen.queryByTestId("podium")).not.toBeInTheDocument();
  });

  test("show the prize column as soon as one row has a prize", () => {
    show(
      standings({
        rows: [
          row({ place: 1, prize: null, label: "alpha" }),
          row({ place: 2, prize: 3, label: "beta" }),
        ],
      }),
      "page",
    );

    expect(screen.getByRole("columnheader", { name: t.columns.prize })).toBeInTheDocument();
    expect(within(tableRowOf("alpha")).getByTestId("prize")).toHaveTextContent(t.prizes.none);
  });

  test("tint a prize row and its edge by its tier", () => {
    show(standings());

    expect(rowOf("alpha")).toHaveClass("bg-gold-wash");
    expect(rowOf("gamma")).toHaveClass("bg-bronze-wash");
    expect(rowOf("delta").className).not.toMatch(/gold|silver|bronze/);
    expect(within(rowOf("beta")).getAllByRole("cell")[0]).toHaveClass("border-l-silver-fill");
  });

  test("say in words which row is yours, not only in colour", () => {
    show(standings());

    const mine = rowOf("beta");
    expect(mine).toHaveAttribute("data-you", "true");
    expect(within(mine).getByText(t.you)).toBeInTheDocument();
    expect(rowOf("alpha")).not.toHaveAttribute("data-you");
  });

  test("draw each score bar against the leader", () => {
    show(standings());

    expect(within(rowOf("alpha")).getByTestId("bar")).toHaveStyle({
      width: "100%",
    });
    expect(within(rowOf("beta")).getByTestId("bar")).toHaveStyle({
      width: "50%",
    });
  });

  test("keep a person's colour between reads", () => {
    show(standings());

    expect(within(rowOf("delta")).getByTestId("initials")).toHaveAttribute(
      "data-hue",
      String(identityHue("delta")),
    );
  });

  test("give the winner in winner mode the first prize, and nobody else any", () => {
    show(
      standings({
        scoring: "winner",
        rows: [
          row({ place: 1, prize: 1, label: "sherlock", winner: true }),
          row({ place: null, label: "watson", points: 50 }),
        ],
      }),
    );

    const sherlock = rowOf("sherlock");
    expect(within(sherlock).getByText(t.winner)).toBeInTheDocument();
    expect(within(sherlock).getByTestId("prize")).toHaveAttribute("data-medal", "gold");
    expect(within(sherlock).getByText(t.prizes.first)).toHaveClass("sr-only");
    expect(within(rowOf("watson")).queryByTestId("prize")).not.toBeInTheDocument();
    expect(within(sherlock).getByTestId("place")).toHaveTextContent("1");
    expect(within(rowOf("watson")).getByTestId("place")).toHaveTextContent(t.unplaced);
  });

  test("stand the winner on the podium in winner mode", () => {
    show(
      standings({
        scoring: "winner",
        rows: [
          row({ place: 1, prize: 1, label: "sherlock", winner: true }),
          row({ place: null, label: "watson", points: 50 }),
        ],
      }),
      "page",
    );

    const podium = screen.getByTestId("podium");
    expect(within(podium).getByText("I")).toBeInTheDocument();
    expect(within(podium).getByText("sherlock")).toBeInTheDocument();
    expect(within(podium).queryByText("watson")).not.toBeInTheDocument();
  });

  test("stand one step per tier on the podium, naming its first row and how many share it", () => {
    show(
      standings({
        rows: [
          row({ place: 1, prize: 1, label: "alpha", points: 40 }),
          row({ place: 2, prize: 1, label: "beta", points: 30 }),
          row({ place: 3, prize: 1, label: "gamma", points: 20 }),
          row({ place: 4, prize: null, label: "delta", points: 10 }),
        ],
      }),
      "page",
    );

    const steps = within(screen.getByTestId("podium")).getAllByTestId("podium-step");
    expect(steps.map((step) => step.dataset.tier ?? "")).toEqual(["", "1", ""]);
    expect(within(steps[1]).getByText("alpha")).toBeInTheDocument();
    expect(within(steps[1]).getByText(t.podiumMore.replace("{count}", "2"))).toBeInTheDocument();
    expect(within(screen.getByTestId("podium")).queryByText("beta")).not.toBeInTheDocument();
  });

  test("stand a lone third prize on its own low step, not the tallest", () => {
    show(standings({ rows: [row({ place: 1, prize: 3, label: "alpha", points: 40 })] }), "page");

    const steps = within(screen.getByTestId("podium")).getAllByTestId("podium-step");
    expect(steps.map((step) => step.dataset.tier ?? "")).toEqual(["", "", "3"]);
    expect(within(steps[2]).getByText("III")).toBeInTheDocument();
  });

  test("never show a deleted account under a login", () => {
    show(standings({ rows: [row({ label: "", deleted: true })] }));

    expect(screen.getByText(t.deleted)).toBeInTheDocument();
  });

  test("say a frozen table is still counting while the contest runs, and who reveals it after", () => {
    const frozenAt = "2026-09-20T11:30:00Z";
    const { unmount } = show(standings({ state: "frozen", frozenAt }));
    const time = formatTime(frozenAt, { locale: "en" });
    expect(screen.getByRole("status")).toHaveTextContent(
      t.frozen.replace("{time}", time),
    );
    expect(screen.getByRole("status")).toHaveTextContent(t.frozenBody);
    unmount();

    show(
      standings({ state: "frozen", frozenAt, endsAt: "2000-01-01T00:00:00Z" }),
    );
    expect(screen.getByRole("status")).toHaveTextContent(t.frozenOver);
  });

  test("show no table before the contest starts, and say why", () => {
    show(standings({ state: "not_started", rows: [] }));

    expect(screen.getByText(t.notStarted)).toBeInTheDocument();
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });

  test("say when nobody is on the table, and when the table was cut", () => {
    const { unmount } = show(standings({ rows: [] }));
    expect(screen.getByText(t.empty)).toBeInTheDocument();
    unmount();

    show(standings({ truncated: true }));
    expect(
      screen.getByText(t.truncated.replace("{count}", "4")),
    ).toBeInTheDocument();
  });

  test("stand only prize winners on the podium, each with their tier's numeral", () => {
    show(
      standings({
        rows: [
          row({ place: 1, prize: 1, label: "alpha", points: 40 }),
          row({ place: 2, prize: 2, label: "beta", points: 20 }),
          row({ place: 3, prize: null, label: "gamma", points: 10 }),
        ],
      }),
      "page",
    );

    const podium = screen.getByTestId("podium");
    expect(within(podium).getByText("I")).toBeInTheDocument();
    expect(within(podium).getByText("II")).toBeInTheDocument();
    expect(within(podium).queryByText("gamma")).not.toBeInTheDocument();
    expect(within(podium).queryByText("1")).not.toBeInTheDocument();
  });

  test("raise the podium on the page, and not in the narrow panel", () => {
    const { unmount } = show(standings(), "page");
    expect(screen.getByTestId("podium")).toBeInTheDocument();
    unmount();

    show(standings(), "panel");
    expect(screen.queryByTestId("podium")).not.toBeInTheDocument();
  });
});

function icpcRow(overrides: Partial<StandingsRow> & { cells: NonNullable<StandingsRow["cells"]> }): StandingsRow {
  return {
    place: 1,
    prize: null,
    label: "ivanov",
    deleted: false,
    points: 0,
    solved: 0,
    penalty: 0,
    lastScoredAt: undefined,
    winner: false,
    isYou: false,
    ...overrides,
  };
}

function icpcStandings(overrides: Partial<Standings> = {}): Standings {
  return {
    state: "live",
    scoring: "icpc",
    title: "The Library Murder",
    frozenAt: undefined,
    endsAt: "2099-01-01T00:00:00Z",
    generatedAt: "2026-09-20T10:05:00Z",
    truncated: false,
    questions: ["A", "B", "C", "D"],
    rows: [
      icpcRow({
        place: 1,
        prize: 1,
        label: "alpha",
        solved: 2,
        penalty: 65,
        cells: [
          { state: "solved", attempts: 2, minute: 47, first: true },
          { state: "failed", attempts: 3 },
          { state: "pending", pending: 2 },
          { state: "untried" },
        ],
      }),
      icpcRow({
        place: 2,
        label: "beta",
        solved: 1,
        penalty: 20,
        cells: [
          { state: "untried" },
          { state: "untried" },
          { state: "pending", attempts: 1, pending: 3 },
          { state: "untried" },
        ],
      }),
    ],
    ...overrides,
  };
}

/** The table row for a label, not the podium's copy of the same name. */
function tableRowOf(label: string): HTMLElement {
  const tr = screen
    .getAllByText(label)
    .map((el) => el.closest("tr"))
    .find((el): el is HTMLTableRowElement => el !== null);
  if (!tr) throw new Error(`No table row found for "${label}"`);
  return tr;
}

describe("the ICPC standings", () => {
  test("show the grid on the page, and not in the narrow panel", () => {
    const { unmount } = show(icpcStandings(), "page");
    expect(screen.getByRole("columnheader", { name: "A" })).toBeInTheDocument();
    expect(screen.getByRole("columnheader", { name: "D" })).toBeInTheDocument();
    unmount();

    show(icpcStandings(), "panel");
    expect(screen.queryByRole("columnheader", { name: "A" })).not.toBeInTheDocument();
  });

  test("show place, participant, solved and penalty columns without a points column", () => {
    show(icpcStandings(), "panel");

    expect(screen.getByText(t.columns.solved)).toBeInTheDocument();
    expect(screen.getByText(t.columns.penalty)).toBeInTheDocument();
    expect(screen.queryByText(t.columns.points)).not.toBeInTheDocument();
    // The panel has no grid, so no cell competes for the same digits.
    const cells = within(rowOf("alpha")).getAllByRole("cell");
    expect(cells.at(-2)).toHaveTextContent("2");
    expect(cells.at(-1)).toHaveTextContent("65");
  });

  test("give a solved cell its attempt count, minute and a spelled-out accessible name", () => {
    show(icpcStandings(), "page");

    expect(screen.getByText("+1")).toBeInTheDocument();
    expect(screen.getByText("47")).toBeInTheDocument();
    expect(
      screen.getByText("A: solved at minute 47 on attempt 2, first to solve"),
    ).toBeInTheDocument();
  });

  test("mark a first solve with a solid fill, not colour alone", () => {
    show(icpcStandings(), "page");

    const cell = screen.getByText("A: solved at minute 47 on attempt 2, first to solve").closest("td");
    expect(cell).toHaveClass("bg-good");
    expect(cell).toHaveClass("text-bg");
  });

  test("give a failed cell its wrong-attempt count and accessible name", () => {
    show(icpcStandings(), "page");

    expect(screen.getByText("−3")).toBeInTheDocument();
    expect(screen.getByText("B: failed, 3 wrong attempts")).toBeInTheDocument();
  });

  test("give a pending cell only the pending mark when nothing was wrong before the freeze", () => {
    show(icpcStandings(), "page");

    const cell = within(tableRowOf("alpha"))
      .getAllByTestId("cell")
      .find((el) => el.dataset.state === "pending") as HTMLElement;
    expect(cell).toBeTruthy();
    expect(within(cell).getByText("?")).toBeInTheDocument();
    expect(within(cell).getByText("2")).toBeInTheDocument();
    expect(within(cell).queryByText(/^−/)).not.toBeInTheDocument();
    expect(screen.getByText("C: 2 attempts after the freeze")).toBeInTheDocument();
  });

  test("give a pending cell both marks when there were wrong attempts before the freeze", () => {
    show(icpcStandings(), "page");

    const cell = within(tableRowOf("beta"))
      .getAllByTestId("cell")
      .find((el) => el.dataset.state === "pending") as HTMLElement;
    expect(cell).toBeTruthy();
    expect(within(cell).getByText("?")).toBeInTheDocument();
    expect(within(cell).getByText("3")).toBeInTheDocument();
    expect(within(cell).getByText("−1")).toBeInTheDocument();
    expect(
      screen.getByText("C: 3 attempts after the freeze, 1 wrong before it"),
    ).toBeInTheDocument();
  });

  test("give an untried cell an accessible name and no visible marks", () => {
    show(icpcStandings(), "page");

    const name = within(tableRowOf("alpha")).getByText("D: untried");
    const cell = name.closest("td") as HTMLElement;
    const visible = cell.querySelector("[aria-hidden]") as HTMLElement;
    expect(visible).toBeEmptyDOMElement();
  });

  test("show solved and penalty under the podium name instead of points", () => {
    show(icpcStandings(), "page");

    expect(within(screen.getByTestId("podium")).getByText("2 · 65")).toBeInTheDocument();
  });

  test("gives the page table a min-width variable scoped to the narrow breakpoint, not an unconditional one", () => {
    const questions = Array.from({ length: 12 }, (_, i) => String.fromCharCode(65 + i));
    const wide = icpcStandings({
      questions,
      rows: [
        icpcRow({
          label: "alpha",
          solved: 1,
          penalty: 10,
          cells: questions.map(() => ({ state: "untried" as const })),
        }),
      ],
    });
    show(wide, "page");

    const table = screen.getByRole("table");
    // Place 3.5 + solved 5 + penalty 5 + 12 grid cells × 3 + name floor 12
    // (rem). Consumed only from `narrow` up, since phones never show the grid.
    expect(table.style.getPropertyValue("--grid-min-width")).toBe("61.5rem");
    expect(table.className).toContain("narrow:min-w-(--grid-min-width)");
    expect(table.style.minWidth).toBe("");
  });

  test("adds the prize column's width to the table's minimum when a prize is shown", () => {
    const questions = Array.from({ length: 12 }, (_, i) => String.fromCharCode(65 + i));
    show(
      icpcStandings({
        questions,
        rows: [
          icpcRow({
            label: "alpha",
            prize: 1,
            solved: 1,
            penalty: 10,
            cells: questions.map(() => ({ state: "untried" as const })),
          }),
        ],
      }),
      "page",
    );

    expect(screen.getByRole("table").style.getPropertyValue("--grid-min-width")).toBe("66rem");
  });

  test("sets no grid-width variable or class in points mode, which has no grid", () => {
    show(standings(), "page");

    const table = screen.getByRole("table");
    expect(table.style.getPropertyValue("--grid-min-width")).toBe("");
    expect(table.className).not.toContain("--grid-min-width");
  });

  test("sets no grid-width variable or class for ICPC on the narrow panel, where there is no grid to protect", () => {
    show(icpcStandings(), "panel");

    const table = screen.getByRole("table");
    expect(table.style.getPropertyValue("--grid-min-width")).toBe("");
    expect(table.className).not.toContain("--grid-min-width");
  });
});

describe.each(["en", "ro", "ru"] as const)("the prize badge in %s", (locale) => {
  test("names each tier, and no prize, to a screen reader in the reader's language", async () => {
    const names = (await getDictionary(locale)).leaderboard.prizes;

    for (const [prize, name] of [
      [1, names.first],
      [2, names.second],
      [3, names.third],
      [null, names.none],
    ] as const) {
      const { unmount } = render(<PrizeBadge prize={prize} names={names} />);
      expect(screen.getByText(name)).toHaveClass("sr-only");
      unmount();
    }
    expect(new Set([names.first, names.second, names.third, names.none]).size).toBe(4);
  });
});

describe.each(["en", "ro", "ru"] as const)("the place badge in %s", (locale) => {
  test("shows the number, or a dash that a screen reader hears as no place", async () => {
    const { unplaced } = (await getDictionary(locale)).leaderboard;

    const { unmount } = render(<PlaceBadge place={7} unplaced={unplaced} />);
    expect(screen.getByTestId("place")).toHaveTextContent("7");
    expect(screen.queryByText(unplaced)).not.toBeInTheDocument();
    unmount();

    render(<PlaceBadge place={null} unplaced={unplaced} />);
    expect(screen.getByText(unplaced)).toHaveClass("sr-only");
    expect(screen.getByText("—")).toHaveAttribute("aria-hidden");
  });

  test("has a column header of its own, apart from the prize's", async () => {
    const { columns } = (await getDictionary(locale)).leaderboard;
    expect(columns.place).toBeTruthy();
    expect(columns.place).not.toBe(columns.prize);
  });
});
