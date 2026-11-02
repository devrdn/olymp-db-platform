import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";

import type { ResultsRow } from "@/lib/api/results";
import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

// The medal is drawn once per row render, which makes it a render counter.
const { prizeBadgeRenders } = vi.hoisted(() => ({ prizeBadgeRenders: { count: 0 } }));
vi.mock(import("@/components/product/standings"), async (original) => {
  const actual = await original();
  return {
    ...actual,
    PrizeBadge: (props: Parameters<typeof actual.PrizeBadge>[0]) => {
      prizeBadgeRenders.count += 1;
      return actual.PrizeBadge(props);
    },
  };
});

import { ParticipantList } from "./participant-list";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

function row(id: string, over: Partial<ResultsRow> = {}): ResultsRow {
  return {
    id,
    place: null,
    prize: null,
    fullName: undefined,
    login: undefined,
    email: undefined,
    deleted: false,
    disqualified: false,
    winner: false,
    solved: 0,
    penalty: 0,
    points: null,
    lastSolvedAt: null,
    bonusCredit: null,
    cells: [],
    ...over,
  };
}

const rows: ResultsRow[] = [
  row("r1", { place: 1, prize: 1, fullName: "Ana Pop", login: "apop", solved: 4, penalty: 120 }),
  row("r2", { place: 2, prize: 2, fullName: "Ion Rus", login: "irus", solved: 3, penalty: 95 }),
  row("r3", { place: 3, fullName: "Maria Ceban", login: "mceban", solved: 2, penalty: 60 }),
  row("r4", { disqualified: true, fullName: "Dan Popa", login: "dpopa", solved: 5, penalty: 10 }),
];

function rowOf(name: string): HTMLElement {
  return screen.getByText(name).closest("li") as HTMLElement;
}

function show(selected: ReadonlySet<string>, onChange = vi.fn()) {
  render(<ParticipantList rows={rows} scoring="icpc" selected={selected} onChange={onChange} dict={dict} />);
  return onChange;
}

describe("the participant list", () => {
  const t = () => dict.leaderboard.export;

  beforeEach(() => {
    prizeBadgeRenders.count = 0;
  });

  test("lists every row with the export's place, a prize medal and the disqualified tag", () => {
    show(new Set(["r1", "r2", "r3"]));

    const ana = rowOf("Ana Pop");
    expect(within(ana).getByTestId("place")).toHaveTextContent("1");
    expect(within(ana).getByTestId("prize")).toHaveAttribute("data-medal", "gold");
    expect(within(ana).getByText("apop")).toBeInTheDocument();
    expect(within(ana).getByText("120")).toBeInTheDocument();
    expect(within(ana).getByRole("checkbox", { name: "Ana Pop" })).toBeChecked();

    const dan = rowOf("Dan Popa");
    expect(within(dan).getByText(dict.leaderboard.staff.disqualified)).toBeInTheDocument();
    expect(within(dan).getByTestId("place")).toHaveTextContent(dict.leaderboard.unplaced);
    expect(within(dan).getByRole("checkbox", { name: "Dan Popa" })).not.toBeChecked();

    expect(screen.getByText(t().ticked.replace("{selected}", "3").replace("{total}", "4"))).toBeInTheDocument();
  });

  test("ticking a row changes that row alone and re-renders no other", async () => {
    function Stateful() {
      const [selected, setSelected] = useState<ReadonlySet<string>>(new Set(["r1"]));
      return <ParticipantList rows={rows} scoring="icpc" selected={selected} onChange={setSelected} dict={dict} />;
    }
    render(<Stateful />);
    const before = prizeBadgeRenders.count;

    await userEvent.click(screen.getByRole("checkbox", { name: "Ion Rus" }));

    expect(screen.getByRole("checkbox", { name: "Ion Rus" })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: "Ana Pop" })).toBeChecked();
    expect(prizeBadgeRenders.count - before).toBe(1);
  });

  test("Tick replaces the selection with the typed range", async () => {
    const onChange = show(new Set(["r1"]));

    await userEvent.type(screen.getByRole("textbox", { name: t().from }), "2");
    await userEvent.type(screen.getByRole("textbox", { name: t().to }), "3");
    await userEvent.click(screen.getByRole("button", { name: t().tick }));

    expect(onChange).toHaveBeenLastCalledWith(new Set(["r2", "r3"]));
  });

  test("Prize winners, All and None replace the selection", async () => {
    const onChange = show(new Set(["r3"]));

    await userEvent.click(screen.getByRole("button", { name: t().prizeWinners }));
    expect(onChange).toHaveBeenLastCalledWith(new Set(["r1", "r2"]));

    await userEvent.click(screen.getByRole("button", { name: t().all }));
    expect(onChange).toHaveBeenLastCalledWith(new Set(["r1", "r2", "r3"]));

    await userEvent.click(screen.getByRole("button", { name: t().none }));
    expect(onChange).toHaveBeenLastCalledWith(new Set());
  });

  test("search hides rows that do not match but keeps their ticks", async () => {
    const onChange = show(new Set(["r1", "r2"]));

    await userEvent.type(screen.getByRole("searchbox", { name: t().search }), "ion");

    expect(screen.getByText("Ion Rus")).toBeInTheDocument();
    expect(screen.queryByText("Ana Pop")).not.toBeInTheDocument();
    expect(onChange).not.toHaveBeenCalled();
    expect(screen.getByText(t().ticked.replace("{selected}", "2").replace("{total}", "4"))).toBeInTheDocument();
  });

  test("says so when the search matches nobody", async () => {
    show(new Set());

    await userEvent.type(screen.getByRole("searchbox", { name: t().search }), "zzz");

    expect(screen.getByText(t().noMatch)).toBeInTheDocument();
  });

  test("keeps a focused row clear of the fixed download bar", () => {
    show(new Set());

    // Scrolled into view by the keyboard, a row stops above the bar the form pins while it is one column.
    expect(screen.getByRole("checkbox", { name: "Ana Pop" }).className).toContain("@max-3xl/export:scroll-mb-36");
  });

  test("shows points rather than penalty outside ICPC", () => {
    render(
      <ParticipantList
        rows={[row("p1", { place: 1, fullName: "Ana Pop", login: "apop", penalty: null, points: 42 })]}
        scoring="points"
        selected={new Set()}
        onChange={vi.fn()}
        dict={dict}
      />,
    );

    expect(within(rowOf("Ana Pop")).getByText("42")).toBeInTheDocument();
    expect(screen.queryByText(dict.leaderboard.columns.penalty)).not.toBeInTheDocument();
  });
});
