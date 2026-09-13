import { render, screen, within } from "@testing-library/react";
import { describe, expect, test } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import {
  identityHue,
  type Standings,
  type StandingsRow,
} from "@/lib/api/leaderboard";
import { formatTime } from "@/lib/format/datetime";

import { StandingsView } from "./standings";

const t = en.leaderboard;

function row(overrides: Partial<StandingsRow>): StandingsRow {
  return {
    place: 1,
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
      row({ place: 1, label: "alpha", points: 40 }),
      row({ place: 2, label: "beta", points: 20, isYou: true }),
      row({ place: 3, label: "gamma", points: 10 }),
      row({ place: 4, label: "delta", points: 5 }),
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
  test("give places one to three a medal that still carries the number", () => {
    show(standings());

    for (const [label, medal, place] of [
      ["alpha", "gold", "1"],
      ["beta", "silver", "2"],
      ["gamma", "bronze", "3"],
    ]) {
      const cell = within(rowOf(label)).getByTestId("place");
      expect(cell).toHaveAttribute("data-medal", medal);
      expect(cell).toHaveTextContent(place);
    }
    expect(within(rowOf("delta")).getByTestId("place")).not.toHaveAttribute(
      "data-medal",
    );
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

  test("name the winner, and show an unplaced row without a number", () => {
    show(
      standings({
        scoring: "winner",
        rows: [
          row({ place: 1, label: "sherlock", winner: true }),
          row({ place: null, label: "watson", points: 50 }),
        ],
      }),
    );

    expect(within(rowOf("sherlock")).getByText(t.winner)).toBeInTheDocument();
    expect(within(rowOf("watson")).getByTestId("place")).toHaveTextContent(
      t.unplaced,
    );
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
        label: "alpha",
        solved: 2,
        penalty: 65,
        cells: [
          { state: "solved", attempts: 2, minute: 47, first: true },
          { state: "failed", attempts: 3 },
          { state: "pending", attempts: 0, pending: 2 },
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

/**
 * The table row for a label, distinguished from the podium's own copy of the
 * same name (both are on screen at once on `page`, since the podium filters
 * to the same placed, scoring rows the table shows).
 */
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
    // The panel never renders the grid, so no cell competes with these cells
    // for the same digits.
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

  test("gives the page table a minimum width covering the grid, so the name column stays legible", () => {
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

    // Place (w-14 = 3.5rem) + solved (w-20 = 5rem) + penalty (w-20 = 5rem)
    // + 12 grid cells (w-12 = 3rem each) + a still-legible 12rem name column.
    expect(screen.getByRole("table")).toHaveStyle({ minWidth: "61.5rem" });
  });

  test("sets no minimum width in points mode, which has no grid", () => {
    show(standings(), "page");

    expect(screen.getByRole("table").style.minWidth).toBe("");
  });

  test("sets no minimum width for ICPC on the narrow panel, where there is no grid to protect", () => {
    show(icpcStandings(), "panel");

    expect(screen.getByRole("table").style.minWidth).toBe("");
  });
});
