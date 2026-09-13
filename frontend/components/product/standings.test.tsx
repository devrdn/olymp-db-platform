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
