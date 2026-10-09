import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { GameDefinition } from "@/lib/api/game";

import { GameBuilder } from "./game-builder";

// The real `GameBuilderTable` calls `router.refresh()`, and there is no app
// router here.
const refresh = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh }) }));

const saved = vi.hoisted(() => ({ current: { saved: true } as { saved?: boolean; code?: string } }));
const saveGameDefinitionAction = vi.hoisted(() => vi.fn(async (_prev: unknown, form: FormData) => ({
  ...saved.current,
  // Exposed so tests can check the definition tree that was sent.
  sentDefinition: JSON.parse(String(form.get("definition"))),
})));

// Each real `GameBuilderTable` fetches its first window on mount.
const gameTableDataWindowAction = vi.hoisted(() =>
  vi.fn(async () => ({ value: { fromRow: 1, rows: [], totalRows: 0, truncated: false } })),
);
const beginTableUploadAction = vi.hoisted(() => vi.fn());
const completeTableUploadAction = vi.hoisted(() => vi.fn());
const abortTableUploadAction = vi.hoisted(() => vi.fn());
const appendTableRowAction = vi.hoisted(() => vi.fn());
const deleteTableRowAction = vi.hoisted(() => vi.fn());

vi.mock("./actions", () => ({
  saveGameDefinitionAction,
  gameTableDataWindowAction,
  beginTableUploadAction,
  completeTableUploadAction,
  abortTableUploadAction,
  appendTableRowAction,
  deleteTableRowAction,
}));

const contestId = "11111111-1111-1111-1111-111111111111";
const tb = en.workspace.game.builder;

const limits: GameDefinition["builderLimits"] = {
  enabled: true,
  chunkBytes: 4194304,
  maxFileBytes: 1073741824,
  maxTables: 2,
  maxTableColumns: 2,
  maxDefinitionBytes: 65536,
  maxFieldBytes: 65536,
  maxLineBytes: 4194304,
  maxRows: 200000,
  maxDeletedRows: 10000,
  columnTypes: ["integer", "text", "date", "timestamp", "numeric", "boolean"],
};

function definition(overrides: Partial<GameDefinition> = {}): GameDefinition {
  return {
    tables: [
      {
        name: "suspects",
        columns: [{ name: "full_name", type: "text", nullable: false }],
        primaryKey: [],
      },
    ],
    builderLimits: limits,
    ...overrides,
  };
}

function show({
  initial = definition(),
  rowCounts = {},
  editable = true,
}: { initial?: GameDefinition; rowCounts?: Record<string, number>; editable?: boolean } = {}) {
  return render(
    <GameBuilder contestId={contestId} definition={initial} rowCounts={rowCounts} editable={editable} dict={en} />,
  );
}

beforeEach(() => {
  saved.current = { saved: true };
  saveGameDefinitionAction.mockClear();
  gameTableDataWindowAction.mockClear();
  vi.spyOn(window, "confirm").mockReturnValue(true);
});

describe("the table builder's own structure editor", () => {
  test("shows the tables and columns already saved", () => {
    show();

    expect(screen.getByDisplayValue("suspects")).toBeInTheDocument();
    expect(screen.getByDisplayValue("full_name")).toBeInTheDocument();
  });

  test("says the game can no longer be replaced once the contest is running", () => {
    show({ editable: false });

    expect(screen.getByText(en.workspace.game.frozen)).toBeInTheDocument();
    expect(screen.getByDisplayValue("suspects")).toBeDisabled();
  });

  // A constant table ceiling would not follow `builder_limits.max_tables` (two
  // here).
  test("stops offering another table once builder_limits.max_tables is reached", async () => {
    show({ initial: definition({ tables: [definition().tables[0], { name: "clues", columns: [{ name: "id", type: "integer", nullable: false }], primaryKey: [] }] }) });

    expect(screen.getByRole("button", { name: tb.addTable })).toBeDisabled();
  });

  test("stops offering another column once builder_limits.max_table_columns is reached for that table", () => {
    show({
      initial: definition({
        tables: [
          {
            name: "suspects",
            columns: [
              { name: "full_name", type: "text", nullable: false },
              { name: "age", type: "integer", nullable: true },
            ],
            primaryKey: [],
          },
        ],
      }),
    });

    expect(screen.getByRole("button", { name: tb.addColumn })).toBeDisabled();
  });

  test("adds a table and a column up to the limit, both enabled by default", async () => {
    const user = userEvent.setup();
    show({ initial: definition({ tables: [] }) });

    await user.click(screen.getByRole("button", { name: tb.addTable }));
    expect(screen.getAllByRole("button", { name: tb.addColumn })).toHaveLength(1);

    await user.click(screen.getByRole("button", { name: tb.addColumn }));
    expect(screen.getAllByRole("textbox")).toHaveLength(2); // the table name, the one column name
  });

  // The server enforces the lock (409); this checks the client half: the row
  // count decides it before any click.
  test("locks a table's name and columns once it already holds data", () => {
    show({ rowCounts: { suspects: 3 } });

    expect(screen.getByText(tb.lockedTable.replace("{n}", "3"))).toBeInTheDocument();
    expect(screen.getByDisplayValue("suspects")).toBeDisabled();
    expect(screen.getByDisplayValue("full_name")).toBeDisabled();
    expect(screen.getByRole("button", { name: tb.removeTable })).toBeDisabled();
    expect(screen.getByRole("button", { name: tb.addColumn })).toBeDisabled();
  });

  test("leaves a table with no rows fully editable", () => {
    show({ rowCounts: { suspects: 0 } });

    expect(screen.queryByText(tb.lockedTable.replace("{n}", "0"))).not.toBeInTheDocument();
    expect(screen.getByDisplayValue("suspects")).not.toBeDisabled();
    expect(screen.getByRole("button", { name: tb.removeTable })).not.toBeDisabled();
  });

  test("saves the structure in the server's own wire shape, name/type/nullable and a primary key by column name", async () => {
    const user = userEvent.setup();
    show({
      initial: definition({
        tables: [
          {
            name: "suspects",
            columns: [{ name: "id", type: "integer", nullable: false }],
            primaryKey: [],
          },
        ],
      }),
    });

    await user.click(screen.getAllByRole("checkbox")[1]); // the primary-key box for "id"
    await user.click(screen.getByRole("button", { name: tb.save }));

    await waitFor(() => expect(screen.getByText(tb.saved)).toBeInTheDocument());
    expect(saveGameDefinitionAction).toHaveBeenCalledTimes(1);
    const sent = JSON.parse(String(saveGameDefinitionAction.mock.calls[0][1].get("definition")));
    expect(sent).toEqual({
      tables: [{ name: "suspects", columns: [{ name: "id", type: "integer", nullable: false }], primary_key: ["id"] }],
    });
  });

  test("a refusal is shown in the organiser's own language, not as a code", async () => {
    const user = userEvent.setup();
    saved.current = { code: "game_definition_duplicate_name" };
    show();

    await user.click(screen.getByRole("button", { name: tb.save }));

    await waitFor(() =>
      expect(screen.getByText(en.errors.game_definition_duplicate_name)).toBeInTheDocument(),
    );
  });

  // A constant size ceiling would not follow this installation's tiny
  // `max_definition_bytes`.
  test("refuses to save once the definition is larger than builder_limits.max_definition_bytes", () => {
    show({ initial: definition({ builderLimits: { ...limits, maxDefinitionBytes: 4 } }) });

    expect(screen.getByRole("button", { name: tb.save })).toBeDisabled();
    expect(screen.getByText(tb.tooLarge)).toBeInTheDocument();
  });
});

describe("the table builder's own data section", () => {
  test("explains there is nothing to add data to yet", () => {
    show({ initial: definition({ tables: [] }) });

    expect(screen.getByText(tb.data.noTables)).toBeInTheDocument();
  });

  test("says table data is off rather than offering a tab nobody can use", () => {
    show({ initial: definition({ builderLimits: { ...limits, enabled: false } }) });

    expect(screen.getByText(tb.data.dataDisabled)).toBeInTheDocument();
  });

  test("offers one tab per table, and switching tabs shows that table's own panel", async () => {
    const user = userEvent.setup();
    show({
      initial: definition({
        tables: [
          { name: "suspects", columns: [{ name: "full_name", type: "text", nullable: false }], primaryKey: [] },
          { name: "clues", columns: [{ name: "id", type: "integer", nullable: false }], primaryKey: [] },
        ],
      }),
    });

    expect(screen.getByRole("tab", { name: "suspects" })).toHaveAttribute("aria-selected", "true");

    await user.click(screen.getByRole("tab", { name: "clues" }));

    expect(screen.getByRole("tab", { name: "clues" })).toHaveAttribute("aria-selected", "true");
    expect(await screen.findByLabelText("id")).toBeVisible();
  });
});
