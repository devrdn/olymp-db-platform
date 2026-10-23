import { render, screen, within } from "@testing-library/react";
import { describe, expect, test, vi } from "vitest";

import type { Standings, StandingsRow } from "@/lib/api/leaderboard";
import en from "@/lib/i18n/dictionaries/en";

const { fetchPublicStandingsAction } = vi.hoisted(() => ({ fetchPublicStandingsAction: vi.fn() }));
vi.mock("./actions", () => ({ fetchPublicStandingsAction }));

import { PublicStandings } from "./public-standings";

function row(overrides: Partial<StandingsRow>): StandingsRow {
  return {
    place: 1, prize: null, label: "ivanov", deleted: false, points: 10, solved: 1,
    penalty: undefined, cells: undefined, lastScoredAt: undefined, winner: false, isYou: false,
    ...overrides,
  };
}

function frozen(rows: StandingsRow[]): Standings {
  return {
    state: "frozen", scoring: "points", title: "The Library Murder", frozenAt: "2026-09-20T11:30:00Z",
    endsAt: "2099-01-01T00:00:00Z", generatedAt: "2026-09-20T11:40:00Z", truncated: false, questions: undefined,
    rows,
  };
}

function tableRowOf(label: string): HTMLElement {
  const tr = screen
    .getAllByText(label)
    .map((el) => el.closest("tr"))
    .find((el): el is HTMLTableRowElement => el !== null);
  if (!tr) throw new Error(`No table row found for "${label}"`);
  return tr;
}

describe("the public standings", () => {
  // The frozen table's tiers are the ones the server gave its frozen places;
  // the page draws them as they come.
  test("show the frozen table's prize tiers, and a dash for everybody else", () => {
    fetchPublicStandingsAction.mockResolvedValue({ kind: "ok", standings: frozen([]) });

    render(
      <PublicStandings
        contestId="c1"
        initial={frozen([
          row({ place: 1, prize: 1, label: "alpha", points: 30 }),
          row({ place: 2, prize: 3, label: "beta", points: 20 }),
          row({ place: 3, prize: null, label: "gamma", points: 10 }),
        ])}
        dict={en}
        locale="en"
      />,
    );

    expect(within(tableRowOf("alpha")).getByTestId("prize")).toHaveAttribute("data-medal", "gold");
    expect(within(tableRowOf("beta")).getByTestId("prize")).toHaveAttribute("data-medal", "bronze");
    const gamma = within(tableRowOf("gamma")).getByTestId("prize");
    expect(gamma).not.toHaveAttribute("data-medal");
    expect(within(gamma).getByText(en.leaderboard.prizes.none)).toHaveClass("sr-only");
    expect(screen.getByRole("columnheader", { name: en.leaderboard.columns.prize })).toBeInTheDocument();
  });
});
