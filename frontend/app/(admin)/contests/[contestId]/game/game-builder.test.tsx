import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, test, vi } from "vitest";
import { answerYesOnFakeClock, onFakeClock } from "@/components/ui/confirm-dialog-test-support";

import en from "@/lib/i18n/dictionaries/en";
import { fill, isolate } from "@/lib/i18n/fill";
import type { GameDefinition } from "@/lib/api/game";

import { GameBuilder } from "./game-builder";

// The real `GameBuilderTable` calls `router.refresh()`, and there is no app
// router here.
const refresh = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", async (importOriginal) => ({
  ...(await importOriginal<typeof import("next/navigation")>()),
  useRouter: () => ({ refresh }),
}));

const saved = vi.hoisted(() => ({ current: { ok: true } as { ok?: true; code?: string } }));
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

function builderAt({
  initial = definition(),
  rowCounts = {},
  editable = true,
  version = 3,
}: { initial?: GameDefinition; rowCounts?: Record<string, number>; editable?: boolean; version?: number } = {}) {
  return (
    <GameBuilder
      contest={{ id: contestId, version }}
      definition={initial}
      rowCounts={rowCounts}
      editable={editable}
      dict={en}
    />
  );
}

function show(options: { initial?: GameDefinition; rowCounts?: Record<string, number>; editable?: boolean } = {}) {
  return render(builderAt(options));
}

beforeEach(() => {
  saved.current = { ok: true };
  saveGameDefinitionAction.mockClear();
  gameTableDataWindowAction.mockClear();
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

    expect(screen.getByText(fill(tb.lockedTable, { n: "3" }))).toBeInTheDocument();
    expect(screen.getByDisplayValue("suspects")).toBeDisabled();
    expect(screen.getByDisplayValue("full_name")).toBeDisabled();
    expect(screen.getByRole("button", { name: tb.removeTable })).toBeDisabled();
    expect(screen.getByRole("button", { name: tb.addColumn })).toBeDisabled();
  });

  test("leaves a table with no rows fully editable", () => {
    show({ rowCounts: { suspects: 0 } });

    expect(screen.queryByText(fill(tb.lockedTable, { n: "0" }))).not.toBeInTheDocument();
    expect(screen.getByDisplayValue("suspects")).not.toBeDisabled();
    expect(screen.getByRole("button", { name: tb.removeTable })).not.toBeDisabled();
  });

  test("asks before removing a table, naming it, and keeps it on cancel", async () => {
    show({ rowCounts: { suspects: 0 } });

    await userEvent.click(screen.getByRole("button", { name: tb.removeTable }));
    expect(await screen.findByRole("dialog", { name: fill(tb.removeTableConfirm, { name: isolate("suspects") }) })).toBeInTheDocument();
    await userEvent.click(screen.getAllByRole("button", { name: en.chrome.cancel })[0]);

    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(screen.getByDisplayValue("suspects")).toBeInTheDocument();
  });

  test("removes the table once the question is answered", () =>
    onFakeClock(async () => {
      show({ rowCounts: { suspects: 0 } });

      fireEvent.click(screen.getByRole("button", { name: tb.removeTable }));
      await answerYesOnFakeClock(tb.removeTable);

      expect(screen.queryByDisplayValue("suspects")).not.toBeInTheDocument();
    }));

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

describe("the table builder's structure, the version it was rendered at", () => {
  test("a save over a contest that moved since says so, and the reload starts the form over", async () => {
    saved.current = { code: "contest_changed" };
    refresh.mockReset();
    show();

    await userEvent.click(screen.getByRole("button", { name: tb.save }));
    expect(await screen.findByRole("alert")).toHaveTextContent(en.errors.contest_changed);
    await userEvent.click(screen.getByRole("button", { name: en.workspace.reload }));

    expect(refresh).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  // The save re-reads the page, whose contest now carries the moved version.
  test("a save sends the version it was rendered at, and the next one the version the page re-read", async () => {
    const view = show();

    await userEvent.click(screen.getByRole("button", { name: tb.save }));
    await waitFor(() => expect(saveGameDefinitionAction).toHaveBeenCalledTimes(1));
    view.rerender(builderAt({ version: 4 }));
    await userEvent.click(screen.getByRole("button", { name: tb.save }));

    await waitFor(() => expect(saveGameDefinitionAction).toHaveBeenCalledTimes(2));
    expect(saveGameDefinitionAction.mock.calls.map(([, form]) => form.get("expectedVersion"))).toEqual(["3", "4"]);
  });
});

// The reload re-reads the page and starts the structure over: the draft held
// in state is the colleague's definition afterwards, not the refused one.
test("a reload after a refused save shows, and sends, the colleague's structure", async () => {
  saved.current = { code: "contest_changed" };
  refresh.mockReset();
  const user = userEvent.setup();
  const view = show();

  const name = screen.getByDisplayValue("suspects");
  await user.clear(name);
  await user.type(name, "mine");
  await user.click(screen.getByRole("button", { name: tb.save }));
  await user.click(await screen.findByRole("button", { name: en.workspace.reload }));
  const theirs = definition({
    tables: [{ name: "witnesses", columns: [{ name: "alibi", type: "text", nullable: false }], primaryKey: [] }],
  });
  view.rerender(builderAt({ initial: theirs, version: 4 }));

  expect(screen.getByDisplayValue("witnesses")).toBeInTheDocument();
  expect(screen.queryByDisplayValue("mine")).not.toBeInTheDocument();
  saved.current = { ok: true };
  saveGameDefinitionAction.mockClear();
  await user.click(screen.getByRole("button", { name: tb.save }));
  await waitFor(() => expect(saveGameDefinitionAction).toHaveBeenCalledTimes(1));
  const form = saveGameDefinitionAction.mock.calls[0][1];
  expect(form.get("expectedVersion")).toBe("4");
  expect(JSON.parse(String(form.get("definition"))).tables[0].name).toBe("witnesses");
});

// A row added below, or a build asked for, moves the version and re-reads the
// page without guarding: an edited structure keeps the version it was based
// on, so a colleague's definition saved meanwhile refuses it instead of being
// written over. An untouched one takes the moved version.
describe("the table builder's structure across an unguarded re-read of the page", () => {
  test("an edited structure keeps the version it was based on", async () => {
    const user = userEvent.setup();
    const view = show();

    await user.type(screen.getByDisplayValue("suspects"), "_2");
    view.rerender(builderAt({ version: 4 }));
    saveGameDefinitionAction.mockClear();
    await user.click(screen.getByRole("button", { name: tb.save }));

    await waitFor(() => expect(saveGameDefinitionAction).toHaveBeenCalledTimes(1));
    expect(saveGameDefinitionAction.mock.calls[0][1].get("expectedVersion")).toBe("3");
  });

  test("a structure changed by its buttons alone keeps it too", async () => {
    const user = userEvent.setup();
    const view = show();

    await user.click(screen.getByRole("button", { name: tb.addTable }));
    view.rerender(builderAt({ version: 4 }));
    saveGameDefinitionAction.mockClear();
    await user.click(screen.getByRole("button", { name: tb.save }));

    await waitFor(() => expect(saveGameDefinitionAction).toHaveBeenCalledTimes(1));
    expect(saveGameDefinitionAction.mock.calls[0][1].get("expectedVersion")).toBe("3");
  });
});

// A newer saved definition reaching an edited structure never replaces it:
// the save is refused, and the reload takes the colleague's structure.
test("an edited structure keeps its draft over a newer definition, is refused, and takes the definition after the reload", async () => {
  saved.current = { code: "contest_changed" };
  const user = userEvent.setup();
  const view = show();
  const theirs = definition({
    tables: [{ name: "witnesses", columns: [{ name: "alibi", type: "text", nullable: false }], primaryKey: [] }],
  });

  await user.type(screen.getByDisplayValue("suspects"), "_2");
  view.rerender(builderAt({ initial: theirs, version: 4 }));
  expect(screen.getByDisplayValue("suspects_2")).toBeInTheDocument();
  saveGameDefinitionAction.mockClear();
  await user.click(screen.getByRole("button", { name: tb.save }));
  await waitFor(() => expect(saveGameDefinitionAction).toHaveBeenCalledTimes(1));
  expect(saveGameDefinitionAction.mock.calls[0][1].get("expectedVersion")).toBe("3");
  await user.click(await screen.findByRole("button", { name: en.workspace.reload }));

  expect(screen.getByDisplayValue("witnesses")).toBeInTheDocument();
  expect(screen.queryByDisplayValue("suspects_2")).not.toBeInTheDocument();
});
