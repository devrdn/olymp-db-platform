import { act, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test, vi } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { ParticipantsTable, ROSTER_ROW_REM } from "./participants-table";
import { NO_FLAGS, rosterRow } from "./test-fixtures";

const CONTEST = "3f1a8c22-1b4e-4a77-9f0d-2c5b8e91a4d6";

let dict: Dictionary;
beforeAll(async () => {
  dict = await getDictionary("en");
});

const rows = [
  rosterRow("r-ivanov", {
    login: "ivanov",
    fullName: "Ivan Ivanov",
    status: "active",
    queries: 120,
    queryErrors: 7,
    queryRejected: 2,
    correct: 3,
    wrong: 1,
    pageLeft: 4,
    awayMs: 95_000,
    pastes: 5,
    ipChanges: 1,
    parallelSessions: 2,
    lastActivity: "2026-09-20T10:14:03.120Z",
    flags: { ...NO_FLAGS, largePaste: true, multipleIps: true },
  }),
  rosterRow("r-petrova", { login: "petrova", fullName: "Anna Petrova", status: "finished", queries: 40 }),
  rosterRow("r-sidorov", { login: "sidorov", fullName: "Pavel Sidorov", status: "registered", queries: 0 }),
];

function renderTable(overrides: Partial<Parameters<typeof ParticipantsTable>[0]> = {}) {
  return render(
    <ParticipantsTable
      contestId={CONTEST}
      rows={rows}
      truncated={false}
      fresh={new Set()}
      dict={dict}
      locale="en"
      {...overrides}
    />,
  );
}

function bodyNames(): string[] {
  const table = screen.getByRole("table");
  const [, ...bodyRows] = within(table).getAllByRole("row");
  return bodyRows.map((row) => within(row).getAllByRole("cell")[0].querySelector("a")?.textContent ?? "");
}

describe("the participants table", () => {
  test("shows each counter, the status and a link to the participant's page", () => {
    renderTable();
    const row = screen.getByRole("row", { name: /Ivan Ivanov/ });
    const cells = within(row).getAllByRole("cell").map((cell) => cell.textContent);

    expect(cells).toEqual(
      expect.arrayContaining(["120", "7", "2", "3", "1", "4", "1:35", "5", "1", "2", "in progress"]),
    );
    expect(within(row).getByRole("link", { name: /Ivan Ivanov/ })).toHaveAttribute(
      "href",
      `/contests/${CONTEST}/monitor/r-ivanov`,
    );
  });

  test("shows a badge per raised flag, each explaining itself", () => {
    renderTable();
    const row = screen.getByRole("row", { name: /Ivan Ivanov/ });

    const paste = within(row).getByText(dict.workspace.monitor.flags.largePaste.label);
    expect(paste.closest("[title]")).toHaveAttribute("title", dict.workspace.monitor.flags.largePaste.explain);
    // Not only a hover title: the explanation is in the badge's text.
    expect(within(row).getByText(dict.workspace.monitor.flags.largePaste.explain)).toBeInTheDocument();
    expect(within(row).getByText(dict.workspace.monitor.flags.multipleIps.label)).toBeInTheDocument();
    expect(within(row).queryByText(dict.workspace.monitor.flags.parallelSessions.label)).not.toBeInTheDocument();
  });

  test("explains the flags, and that they are not proof, beside the table", () => {
    renderTable();
    const tooltip = screen.getByRole("tooltip", { hidden: true });

    expect(tooltip).toHaveTextContent(dict.workspace.monitor.flags.notProof);
    expect(tooltip).toHaveTextContent(dict.workspace.monitor.flags.identicalQueries.explain);
  });

  test("lights the row of a participant with something new", () => {
    renderTable({ fresh: new Set(["r-petrova"]) });

    const lit = screen.getByRole("row", { name: /Anna Petrova/ });
    expect(lit).toHaveAttribute("data-fresh", "true");
    expect(screen.getByRole("row", { name: /Ivan Ivanov/ })).not.toHaveAttribute("data-fresh");
    // The whole row, not only the held name cell.
    expect(lit).toHaveClass("data-fresh:bg-accent-wash");
  });
});

describe("sorting", () => {
  test("is by name to begin with, and by any column on a press", async () => {
    renderTable();
    expect(bodyNames()).toEqual(["Anna Petrova", "Ivan Ivanov", "Pavel Sidorov"]);

    const queries = screen.getByRole("button", { name: dict.workspace.monitor.table.columns.queries });
    await userEvent.click(queries);
    expect(bodyNames()).toEqual(["Ivan Ivanov", "Anna Petrova", "Pavel Sidorov"]);
    expect(queries.closest("th")).toHaveAttribute("aria-sort", "descending");

    await userEvent.click(queries);
    expect(bodyNames()).toEqual(["Pavel Sidorov", "Anna Petrova", "Ivan Ivanov"]);
    expect(queries.closest("th")).toHaveAttribute("aria-sort", "ascending");
  });
});

describe("filters", () => {
  const t = () => dict.workspace.monitor.table;

  test("to the flagged only", async () => {
    renderTable();
    await userEvent.click(screen.getByRole("checkbox", { name: t().filters.flaggedOnly }));

    expect(bodyNames()).toEqual(["Ivan Ivanov"]);
    expect(screen.getByText(t().count.replace("{shown}", "1").replace("{total}", "3"))).toBeInTheDocument();
  });

  test("by status", async () => {
    renderTable();
    await userEvent.selectOptions(screen.getByRole("combobox", { name: t().filters.status }), "finished");

    expect(bodyNames()).toEqual(["Anna Petrova"]);
  });

  test("by a piece of the name or the login", async () => {
    renderTable();
    await userEvent.type(screen.getByRole("searchbox", { name: t().filters.search }), "SIDOR");

    expect(bodyNames()).toEqual(["Pavel Sidorov"]);
  });

  test("says so when nobody matches", async () => {
    renderTable();
    await userEvent.type(screen.getByRole("searchbox", { name: t().filters.search }), "nobody");

    expect(screen.getByText(t().noMatch)).toBeInTheDocument();
  });
});

test("says when the table is cut short", () => {
  renderTable({ truncated: true });
  expect(screen.getByText(dict.workspace.monitor.table.truncated.replace("{n}", "3"))).toBeInTheDocument();
});

describe("a long roster", () => {
  const ROW_PX = ROSTER_ROW_REM * 16;
  const VIEW_PX = ROW_PX * 12;
  const TOTAL = 2000;

  /** Names sort in the order of their number; queries run the other way. */
  const long = Array.from({ length: TOTAL }, (_, i) => {
    const n = String(i).padStart(4, "0");
    return rosterRow(`r-${n}`, {
      login: `p${n}`,
      fullName: `Person ${n}`,
      status: i % 2 === 0 ? "active" : "finished",
      queries: TOTAL - i,
      flags: { ...NO_FLAGS, largePaste: i % 10 === 0 },
    });
  });

  /** jsdom has no layout: the box gets a height and the test sets its offset. */
  function scroller(): HTMLElement {
    const element = screen.getByTestId("roster-scroller");
    Object.defineProperty(element, "clientHeight", { value: VIEW_PX, configurable: true });
    return element;
  }

  function scrollTo(element: HTMLElement, top: number) {
    act(() => {
      element.scrollTop = top;
      fireEvent.scroll(element);
    });
  }

  function bodyRows(): HTMLElement[] {
    const [, ...rest] = within(screen.getByRole("table")).getAllByRole("row");
    return rest;
  }

  test("renders a bounded number of rows before it can measure, as the server does", () => {
    renderTable({ rows: long });
    expect(bodyRows().length).toBeLessThanOrEqual(24);
  });

  test("renders only the rows in view, and still reports the whole table", () => {
    renderTable({ rows: long });
    scrollTo(scroller(), 1000 * ROW_PX);

    const shown = bodyRows();
    expect(shown.length).toBeLessThanOrEqual(12 + 2 * 8 + 1);
    expect(screen.getByRole("table")).toHaveAttribute("aria-rowcount", String(TOTAL + 1));
    // The heading is row 1, so the participant at index 1000 is row 1002.
    const row = screen.getByRole("row", { name: /Person 1000/ });
    expect(row).toHaveAttribute("aria-rowindex", "1002");
    expect(screen.queryByRole("row", { name: /Person 0000/ })).not.toBeInTheDocument();
  });

  test("keeps the place the scroll offset says, so the spacer rows hold the full height", () => {
    renderTable({ rows: long });
    scrollTo(scroller(), 1000 * ROW_PX);

    const [before, after] = screen
      .getByRole("table")
      .querySelectorAll<HTMLElement>("tbody > tr[data-spacer]");
    const first = Number(bodyRows()[0].getAttribute("aria-rowindex")) - 2;
    const last = Number(bodyRows().at(-1)?.getAttribute("aria-rowindex")) - 2;
    expect(before.style.height).toBe(`${first * ROSTER_ROW_REM}rem`);
    expect(after.style.height).toBe(`${(TOTAL - 1 - last) * ROSTER_ROW_REM}rem`);
  });

  test("sorts the whole roster, not only the rows on screen", async () => {
    renderTable({ rows: long });
    const box = scroller();
    scrollTo(box, 0);

    await userEvent.click(screen.getByRole("button", { name: dict.workspace.monitor.table.columns.queries }));

    // Most queries first: the last participant by name.
    expect(bodyNames()[0]).toBe("Person 0000");
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.monitor.table.columns.queries }));
    expect(bodyNames()[0]).toBe("Person 1999");
    expect(bodyRows().length).toBeLessThanOrEqual(12 + 2 * 8 + 1);
  });

  test("filters the whole roster and counts what matched", async () => {
    renderTable({ rows: long });
    scroller();

    await userEvent.click(screen.getByRole("checkbox", { name: dict.workspace.monitor.table.filters.flaggedOnly }));

    const t = dict.workspace.monitor.table;
    expect(screen.getByText(t.count.replace("{shown}", "200").replace("{total}", String(TOTAL)))).toBeInTheDocument();
    expect(screen.getByRole("table")).toHaveAttribute("aria-rowcount", "201");
    expect(bodyNames().slice(0, 3)).toEqual(["Person 0000", "Person 0010", "Person 0020"]);
    expect(bodyRows().length).toBeLessThanOrEqual(12 + 2 * 8 + 1);
  });

  test("keeps each column as wide as its widest row anywhere in the list", () => {
    const widest = { ...NO_FLAGS, largePaste: true, identicalQueries: true, answerWithoutQueries: true };
    renderTable({ rows: [...long.slice(0, TOTAL - 1), rosterRow("r-last", { fullName: "Zoe Last", flags: widest })] });

    // Far below the window, yet its badges size the column.
    expect(screen.queryByRole("row", { name: /Zoe Last/ })).not.toBeInTheDocument();
    const sizer = screen.getByRole("table").querySelector<HTMLElement>("tbody > tr[data-sizer]");
    expect(sizer).toHaveAttribute("aria-hidden", "true");
    expect(sizer).toHaveTextContent(dict.workspace.monitor.flags.answerWithoutQueries.label);
    expect(sizer).toHaveTextContent(dict.workspace.monitor.flags.identicalQueries.label);
  });

  test("lets the keyboard walk from the column headings into the rows", async () => {
    renderTable({ rows: long });
    const lastHeading = screen.getByRole("button", { name: dict.workspace.monitor.table.columns.lastActivity });
    lastHeading.focus();

    await userEvent.tab();
    expect(screen.getByRole("link", { name: /Person 0000/ })).toHaveFocus();
    await userEvent.tab();
    expect(screen.getByRole("link", { name: /Person 0001/ })).toHaveFocus();
  });

  /** PageDown or the wheel scrolls the box under a focused link; focus must not fall to the page. */
  test("keeps the focused row, and its focus, when the box scrolls far past it", () => {
    renderTable({ rows: long });
    const box = scroller();
    const link = screen.getByRole("link", { name: /Person 0003/ });
    act(() => link.focus());

    scrollTo(box, 1000 * ROW_PX);

    expect(screen.getByRole("link", { name: /Person 0003/ })).toHaveFocus();
    expect(screen.getByRole("row", { name: /Person 0003/ })).toHaveAttribute("aria-rowindex", "5");
    expect(screen.getByRole("row", { name: /Person 1000/ })).toBeInTheDocument();
    // The spacers around the held row still add up to the whole list.
    const spacers = [...screen.getByRole("table").querySelectorAll<HTMLElement>("tbody > tr[data-spacer]")];
    const spaced = spacers.reduce((sum, row) => sum + parseFloat(row.style.height || "0"), 0) / ROSTER_ROW_REM;
    expect(Math.round(spaced) + bodyRows().length).toBe(TOTAL);
  });

  test("moves Tab from a focused row scrolled out of view to the row truly after it", async () => {
    renderTable({ rows: long });
    const box = scroller();
    act(() => screen.getByRole("link", { name: /Person 0003/ }).focus());
    scrollTo(box, 1000 * ROW_PX);

    await userEvent.tab();
    expect(screen.getByRole("link", { name: /Person 0004/ })).toHaveFocus();
    await userEvent.tab({ shift: true });
    expect(screen.getByRole("link", { name: /Person 0003/ })).toHaveFocus();
  });

  /**
   * jsdom keeps focus on a node that is moved; Chromium blurs it, as a removal
   * and an insertion. This makes jsdom do the same for the rest of a test.
   */
  function blurOnMove() {
    for (const method of ["insertBefore", "appendChild"] as const) {
      const original = Node.prototype[method] as (this: Node, ...args: Node[]) => Node;
      vi.spyOn(Node.prototype, method).mockImplementation(function (this: Node, ...args: Node[]) {
        const [node] = args;
        const active = document.activeElement;
        if (node.isConnected && active instanceof HTMLElement && node.contains(active)) active.blur();
        return original.apply(this, args);
      });
    }
  }

  test("keeps the focused row, and its focus, when a poll sorts it out of view", async () => {
    blurOnMove();
    const view = renderTable({ rows: long });
    scroller();
    await userEvent.click(screen.getByRole("button", { name: dict.workspace.monitor.table.columns.queries }));
    const link = screen.getByRole("link", { name: /Person 0000/ });
    act(() => link.focus());

    // Their queries drop to nothing: most queries first, they now sort last.
    const resorted = [{ ...long[0], queries: 0 }, ...long.slice(1)];
    view.rerender(
      <ParticipantsTable contestId={CONTEST} rows={resorted} truncated={false} fresh={new Set()} dict={dict} locale="en" />,
    );

    expect(screen.getByRole("link", { name: /Person 0000/ })).toHaveFocus();
    expect(screen.getByRole("row", { name: /Person 0000/ })).toHaveAttribute("aria-rowindex", String(TOTAL + 1));
    expect(bodyNames()[0]).toBe("Person 0001");
    vi.restoreAllMocks();
  });

  test("lets a row go once focus has left the table", () => {
    renderTable({ rows: long });
    const box = scroller();
    act(() => screen.getByRole("link", { name: /Person 0003/ }).focus());
    act(() => screen.getByRole("searchbox", { name: dict.workspace.monitor.table.filters.search }).focus());

    scrollTo(box, 1000 * ROW_PX);

    expect(screen.queryByRole("row", { name: /Person 0003/ })).not.toBeInTheDocument();
  });
});
