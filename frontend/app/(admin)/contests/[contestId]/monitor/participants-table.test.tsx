import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, describe, expect, test } from "vitest";

import { getDictionary, type Dictionary } from "@/lib/i18n/dictionary";

import { ParticipantsTable } from "./participants-table";
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
