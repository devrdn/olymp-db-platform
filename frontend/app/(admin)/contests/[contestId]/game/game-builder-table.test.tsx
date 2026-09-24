import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { BuilderLimits, TableData, TableDefinition } from "@/lib/api/game";

import { GameBuilderTable } from "./game-builder-table";

// `router.refresh()` is what brings `GameBuild`'s own `game` prop — read by
// `page.tsx`, a server component, at page load — back from the server after a
// row is written. Without it the notice this whole feature exists to show can
// only ever appear on the next reload, and an organiser who types fifty rows
// and closes the tab is never told the game is out of date.
const refresh = vi.hoisted(() => vi.fn());
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh }) }));

const beginTableUploadAction = vi.hoisted(() => vi.fn());
const currentTableUploadAction = vi.hoisted(() => vi.fn());
const completeTableUploadAction = vi.hoisted(() => vi.fn());
const abortTableUploadAction = vi.hoisted(() => vi.fn());
const gameTableDataWindowAction = vi.hoisted(() => vi.fn());
const appendTableRowAction = vi.hoisted(() => vi.fn());
const deleteTableRowAction = vi.hoisted(() => vi.fn());

vi.mock("./actions", () => ({
  beginTableUploadAction,
  currentTableUploadAction,
  completeTableUploadAction,
  abortTableUploadAction,
  gameTableDataWindowAction,
  appendTableRowAction,
  deleteTableRowAction,
}));

// `request` (the direct-to-API chunk transport) is the one thing this
// component talks to that is not a Server Action — `game-upload.test.tsx`'s
// own doc gives the identical reason.
const request = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api/client")>();
  return { ...actual, request };
});

import { ApiError } from "@/lib/api/client";

const contestId = "11111111-1111-1111-1111-111111111111";
const dataId = "33333333-3333-3333-3333-333333333333";
const td = en.workspace.game.builder.data;

// A chunk size unlikely to appear by coincidence if the component ever falls
// back to a hardcoded number instead of reading `builder_limits.chunk_bytes`
// — the same tactic `game-upload.test.tsx` uses for the dump's own ceiling.
const limits: BuilderLimits = {
  enabled: true,
  chunkBytes: 5,
  maxFileBytes: 4 * 1024 * 1024 * 1024,
  maxTables: 50,
  maxTableColumns: 50,
  maxDefinitionBytes: 65536,
  maxFieldBytes: 65536,
  maxLineBytes: 4194304,
  maxRows: 200000,
  maxDeletedRows: 10000,
  columnTypes: ["integer", "text", "date", "timestamp", "numeric", "boolean"],
};

const table: TableDefinition = {
  name: "suspects",
  columns: [
    { name: "full_name", type: "text", nullable: false },
    { name: "age", type: "integer", nullable: true },
  ],
  primaryKey: [],
};

function tableData(overrides: Partial<TableData> = {}): TableData {
  return {
    id: dataId,
    table: "suspects",
    declaredBytes: 12,
    receivedBytes: 0,
    lines: 0,
    activeRows: 0,
    deletedRows: [],
    status: "receiving",
    createdAt: "2026-03-01T09:00:00Z",
    updatedAt: "2026-03-01T09:00:00Z",
    builderLimits: limits,
    ...overrides,
  };
}

function show({
  active = true,
  editable = true,
  enabled = true,
  onRowCountChange = vi.fn(),
  activeRowCount = 0,
  onActiveRowCountChange = vi.fn(),
  initialTableData = null,
}: {
  active?: boolean;
  editable?: boolean;
  enabled?: boolean;
  onRowCountChange?: (n: number) => void;
  /** The table's current active (deletion-adjusted) row count — the
   * counterpart of the count `onRowCountChange` reports (`Lines`, never
   * reduced by a delete: `GameBuilder`'s own lock keys off it exactly
   * because a tombstoned row still leaves the file's old header behind),
   * while this is `ActiveRows`, the number this screen shows. */
  activeRowCount?: number;
  onActiveRowCountChange?: (n: number) => void;
  /** The chunked upload a reloaded page found still receiving, or null —
   * `initialUpload` in `game-upload.tsx`, for a table's own CSV instead of
   * a dump. */
  initialTableData?: ReturnType<typeof tableData> | null;
} = {}) {
  return render(
    <GameBuilderTable
      contestId={contestId}
      table={table}
      active={active}
      limits={{ ...limits, enabled }}
      editable={editable}
      onRowCountChange={onRowCountChange}
      activeRowCount={activeRowCount}
      onActiveRowCountChange={onActiveRowCountChange}
      initialTableData={initialTableData}
      dict={en}
    />,
  );
}

beforeEach(() => {
  beginTableUploadAction.mockReset();
  currentTableUploadAction.mockReset();
  completeTableUploadAction.mockReset();
  abortTableUploadAction.mockReset();
  gameTableDataWindowAction.mockReset();
  appendTableRowAction.mockReset();
  deleteTableRowAction.mockReset();
  request.mockReset();
  refresh.mockClear();
  vi.spyOn(window, "confirm").mockReturnValue(true);

  // Every test renders a table whose tab is `active` by default, which
  // fetches the first window the instant it mounts (this component's own
  // doc explains why, unlike `GameUpload`, that fetch is not gated on the
  // upload's own phase). An empty page is the harmless default a test that
  // is not itself exercising the row list should not have to restate;
  // `mockResolvedValueOnce` calls inside a test still take priority over it
  // for that test's own first call.
  gameTableDataWindowAction.mockResolvedValue({
    value: { fromRow: 1, rows: [], totalRows: 0, truncated: false },
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the table builder's own data panel", () => {
  test("says table data is off rather than offering controls nobody can use", () => {
    show({ enabled: false });

    expect(screen.getByText(td.dataDisabled)).toBeInTheDocument();
    expect(screen.queryByText(td.pick)).not.toBeInTheDocument();
  });

  test("loads the first page only once the tab is actually looked at", async () => {
    show({ active: false });
    expect(gameTableDataWindowAction).not.toHaveBeenCalled();
  });

  test("shows the table has no rows yet", async () => {
    gameTableDataWindowAction.mockResolvedValueOnce({
      value: { fromRow: 1, rows: [], totalRows: 0, truncated: false },
    });

    show();

    expect(await screen.findByText(td.emptyRows)).toBeInTheDocument();
  });

  // A reload mid-upload finds the unfinished upload the same way
  // `game-upload.tsx` finds a dump's own: seeded from `initialTableData`,
  // the prop `page.tsx` reads from `GET .../tables/{table}/data/current`.
  // That route is what closed the gap this component's own doc used to name
  // — the component still never calls it itself, which is why the state
  // arrives as a prop rather than as a fetch on mount.
  test("shows an unfinished upload and asks for a file of the same size to continue", () => {
    show({ initialTableData: tableData({ receivedBytes: 5, declaredBytes: 12 }) });

    expect(screen.getByText(td.resumeHeading)).toBeInTheDocument();
    expect(
      screen.getByText(td.resumeBody.replace("{received}", "5 B").replace("{total}", "12 B")),
    ).toBeInTheDocument();
  });

  // The mistake this guards against is resuming with a file that only looks
  // right by name — this upload carries no filename at all (unlike a dump's
  // own), so the one thing worth checking before spending a request is size.
  test("refuses to resume with a file whose size does not match the unfinished upload", async () => {
    show({ initialTableData: tableData({ receivedBytes: 5, declaredBytes: 12 }) });
    const wrongFile = new File([new Uint8Array(3)], "other.csv");

    await userEvent.upload(screen.getByLabelText(td.resumePick), wrongFile);

    expect(screen.getByText(td.resumeMismatch.replace("{total}", "12 B"))).toBeInTheDocument();
    expect(beginTableUploadAction).not.toHaveBeenCalled();
    expect(currentTableUploadAction).not.toHaveBeenCalled();
  });

  // The brief's own rule for a resumed upload, proven here the same way
  // "resumes the next chunk from the server's own received_bytes" proves it
  // for a fresh one below: the next chunk continues from what the server
  // reports *now* (`currentTableUploadAction`), not from the possibly stale
  // count the page loaded with.
  test("resumes an unfinished upload from the server's own current offset, not the one the page loaded with", async () => {
    currentTableUploadAction.mockResolvedValueOnce(tableData({ receivedBytes: 7, declaredBytes: 12 }));
    request.mockResolvedValueOnce({ received_bytes: 12 });
    completeTableUploadAction.mockResolvedValueOnce({ value: tableData({ status: "complete", activeRows: 1 }) });

    // The page loaded with 5 bytes received; the server has actually taken 7
    // by the time this tab resumes — a chunk sent from this page's own, now
    // stale, count would land out of order.
    show({ initialTableData: tableData({ receivedBytes: 5, declaredBytes: 12 }) });
    const file = new File([new Uint8Array(12)], "suspects.csv");

    await userEvent.upload(screen.getByLabelText(td.resumePick), file);

    await waitFor(() => expect(request).toHaveBeenCalledTimes(1));
    expect(request).toHaveBeenNthCalledWith(
      1,
      `/contests/${contestId}/game/tables/suspects/data/${dataId}/chunk?offset=7`,
      expect.objectContaining({ method: "PUT" }),
    );
    expect(beginTableUploadAction).not.toHaveBeenCalled();
  });

  // The mutation this guards against: a chunk size taken from a constant
  // instead of `builder_limits.chunk_bytes` would still finish a 12-byte
  // file, but never in three pieces of 5, 5 and 2.
  test("sends a small file in chunks sized from builder_limits, then shows what was received", async () => {
    beginTableUploadAction.mockResolvedValueOnce({ value: tableData({ receivedBytes: 0 }) });
    let received = 0;
    request.mockImplementation(async (_path: string, options: { rawBody?: Blob }) => {
      received += options.rawBody?.size ?? 0;
      return { received_bytes: received };
    });
    completeTableUploadAction.mockResolvedValueOnce({ value: tableData({ status: "complete", activeRows: 1 }) });
    gameTableDataWindowAction.mockResolvedValue({
      value: { fromRow: 1, rows: [{ row: 1, fields: ["Ada", "37"] }], totalRows: 1, truncated: false },
    });

    show();
    const file = new File([new Uint8Array(12)], "suspects.csv");

    await userEvent.upload(screen.getByLabelText(td.pick), file);

    await waitFor(() => expect(request).toHaveBeenCalledTimes(3));

    expect(beginTableUploadAction).toHaveBeenCalledWith(contestId, "suspects", 12);
    const [urls, sizes] = [
      request.mock.calls.map((call) => call[0]),
      request.mock.calls.map((call) => (call[1] as { rawBody?: Blob }).rawBody?.size),
    ];
    expect(urls).toEqual([
      `/contests/${contestId}/game/tables/suspects/data/${dataId}/chunk?offset=0`,
      `/contests/${contestId}/game/tables/suspects/data/${dataId}/chunk?offset=5`,
      `/contests/${contestId}/game/tables/suspects/data/${dataId}/chunk?offset=10`,
    ]);
    expect(sizes).toEqual([5, 5, 2]);
    await waitFor(() => expect(completeTableUploadAction).toHaveBeenCalledWith(contestId, "suspects", dataId));
  });

  // The brief's own rule, for a table's own upload exactly as much as for
  // the whole game's dump: the next chunk continues from the server's own
  // received_bytes, never from this tab's own running total.
  test("resumes the next chunk from the server's own received_bytes, not from what this tab sent", async () => {
    beginTableUploadAction.mockResolvedValueOnce({ value: tableData({ receivedBytes: 0, declaredBytes: 12 }) });
    request.mockResolvedValueOnce({ received_bytes: 7 }).mockResolvedValueOnce({ received_bytes: 12 });
    completeTableUploadAction.mockResolvedValueOnce({ code: "unreachable" });

    show();
    const file = new File([new Uint8Array(12)], "suspects.csv");
    await userEvent.upload(screen.getByLabelText(td.pick), file);

    await waitFor(() => expect(request).toHaveBeenCalledTimes(2));

    expect(request).toHaveBeenNthCalledWith(
      2,
      `/contests/${contestId}/game/tables/suspects/data/${dataId}/chunk?offset=7`,
      expect.objectContaining({ method: "PUT" }),
    );
  });

  test("shows the server's own refusal when a chunk is rejected, and offers to retry", async () => {
    beginTableUploadAction.mockResolvedValueOnce({ value: tableData({ receivedBytes: 0 }) });
    request.mockRejectedValueOnce(new ApiError("game_table_data_chunk_too_large", 400, "too big"));

    show();
    const file = new File([new Uint8Array(12)], "suspects.csv");
    await userEvent.upload(screen.getByLabelText(td.pick), file);

    await waitFor(() =>
      expect(screen.getByText(en.errors.game_table_data_chunk_too_large)).toBeInTheDocument(),
    );
    expect(screen.getByRole("button", { name: td.retry })).toBeInTheDocument();
  });

  // The brief's own requirement: a CSV refusal names the row and column at
  // fault, and this screen has to show that rather than only the generic
  // dictionary sentence — `en.errors.game_table_row_field_count`'s own text
  // says as much ("The message says which row"). The header check runs on
  // the chunk PUT itself (`checkTableHeaderOnFirstChunk`), which this
  // component talks to directly rather than through a Server Action, so the
  // detail here comes straight off the thrown `ApiError`.
  test("shows the server's own row and column when a chunk fails header validation, not only the generic sentence", async () => {
    beginTableUploadAction.mockResolvedValueOnce({ value: tableData({ receivedBytes: 0 }) });
    request.mockRejectedValueOnce(
      new ApiError(
        "game_table_header_mismatch",
        400,
        'the file has 1 column(s), the table has 2: column 1 is "name", want "full_name"',
      ),
    );

    show();
    const file = new File([new Uint8Array(12)], "suspects.csv");
    await userEvent.upload(screen.getByLabelText(td.pick), file);

    await waitFor(() =>
      expect(screen.getByText(en.errors.game_table_header_mismatch)).toBeInTheDocument(),
    );
    expect(
      screen.getByText('the file has 1 column(s), the table has 2: column 1 is "name", want "full_name"'),
    ).toBeInTheDocument();
  });

  // The same requirement, for a row typed into the form instead of a CSV
  // file — completeTableUploadAction and appendTableRowAction both carry the
  // server's own row/column detail back through `UploadActionResult.detail`
  // (`actions.ts`'s own doc), since those two calls go through a Server
  // Action rather than a direct `request()` this component can read
  // `ApiError.message` off itself.
  test("shows the server's own row and column when adding a row is refused, not only the generic sentence", async () => {
    gameTableDataWindowAction.mockResolvedValueOnce({
      value: { fromRow: 1, rows: [], totalRows: 0, truncated: false },
    });
    appendTableRowAction.mockResolvedValueOnce({
      code: "game_table_value_invalid",
      detail: 'row 0, column "age": "old" is not a whole number that fits a 32-bit integer',
    });

    show();
    await screen.findByText(td.emptyRows);

    await userEvent.type(screen.getByLabelText("full_name"), "Ada");
    await userEvent.type(screen.getByLabelText("age (empty = NULL)"), "old");
    await userEvent.click(screen.getByRole("button", { name: td.addRowButton }));

    await waitFor(() =>
      expect(
        screen.getByText('row 0, column "age": "old" is not a whole number that fits a 32-bit integer'),
      ).toBeInTheDocument(),
    );
    expect(screen.getByText(en.errors.game_table_value_invalid)).toBeInTheDocument();
  });

  test("cancels an upload in progress and lets the server know", async () => {
    beginTableUploadAction.mockResolvedValueOnce({ value: tableData({ receivedBytes: 0 }) });
    let chunkSignal: AbortSignal | undefined;
    request.mockImplementationOnce((_path: string, init: { signal: AbortSignal }) => {
      chunkSignal = init.signal;
      return new Promise((_resolve, reject) => {
        init.signal.addEventListener("abort", () =>
          reject(new DOMException("The operation was aborted.", "AbortError")),
        );
      });
    });
    abortTableUploadAction.mockResolvedValueOnce({ value: tableData({ status: "aborted" }) });

    show();
    const file = new File([new Uint8Array(12)], "suspects.csv");
    await userEvent.upload(screen.getByLabelText(td.pick), file);

    const cancelButton = await screen.findByRole("button", { name: td.cancel });
    await waitFor(() => expect(chunkSignal).toBeDefined());
    await userEvent.click(cancelButton);

    expect(window.confirm).toHaveBeenCalledWith(td.cancelConfirm);
    expect(chunkSignal?.aborted).toBe(true);
    await waitFor(() => expect(abortTableUploadAction).toHaveBeenCalledWith(contestId, "suspects", dataId));
    expect(screen.getByLabelText(td.pick)).toBeInTheDocument();
  });

  test("adds a row typed into the form, one input per column", async () => {
    gameTableDataWindowAction.mockResolvedValueOnce({
      value: { fromRow: 1, rows: [], totalRows: 0, truncated: false },
    });
    appendTableRowAction.mockResolvedValueOnce({ value: tableData({ status: "complete", activeRows: 1 }) });
    gameTableDataWindowAction.mockResolvedValueOnce({
      value: { fromRow: 1, rows: [{ row: 1, fields: ["Ada", "37"] }], totalRows: 1, truncated: false },
    });

    show();
    await screen.findByText(td.emptyRows);

    await userEvent.type(screen.getByLabelText("full_name"), "Ada");
    await userEvent.type(screen.getByLabelText("age (empty = NULL)"), "37");
    await userEvent.click(screen.getByRole("button", { name: td.addRowButton }));

    await waitFor(() =>
      expect(appendTableRowAction).toHaveBeenCalledWith(contestId, "suspects", ["Ada", "37"]),
    );
    expect(await screen.findByText("Ada")).toBeInTheDocument();
  });

  // `TableData.Lines` and `TableData.ActiveRows()` answer two different
  // questions (`tabledata.go`'s own doc: a tombstoned row leaves the file's
  // old header behind, so `GameBuilder`'s own structure lock has to key off
  // `Lines`, never off the deletion-adjusted count) — a write that changes
  // both has to report both, to the two separate callbacks that carry them,
  // rather than one shared count a later window fetch and an earlier write
  // overwrite each other's meaning in.
  test("reports a row's own total and its active count separately, not as one shared number", async () => {
    gameTableDataWindowAction.mockResolvedValueOnce({
      value: { fromRow: 1, rows: [], totalRows: 10, truncated: false },
    });
    // 11 total (the file's own line count, never reduced by a tombstone) and
    // 8 active — the brief's own numbers: ten rows, three already deleted,
    // one just added.
    appendTableRowAction.mockResolvedValueOnce({ value: tableData({ status: "complete", lines: 11, activeRows: 8 }) });
    gameTableDataWindowAction.mockResolvedValueOnce({
      value: { fromRow: 1, rows: [{ row: 11, fields: ["Ada", "37"] }], totalRows: 11, truncated: false },
    });

    const onRowCountChange = vi.fn();
    const onActiveRowCountChange = vi.fn();
    show({ onRowCountChange, onActiveRowCountChange });
    await screen.findByText(td.emptyRows);

    await userEvent.type(screen.getByLabelText("full_name"), "Ada");
    await userEvent.type(screen.getByLabelText("age (empty = NULL)"), "37");
    await userEvent.click(screen.getByRole("button", { name: td.addRowButton }));

    await waitFor(() => expect(onActiveRowCountChange).toHaveBeenCalledWith(8));
    // The structural-lock count must reach 11 (Lines), from this write
    // itself — not only once the follow-up window fetch happens to agree
    // (that fetch's own 11 would mask a write that reported the wrong
    // number here).
    expect(onRowCountChange).toHaveBeenCalledWith(11);
    expect(onRowCountChange).not.toHaveBeenCalledWith(8);
  });

  // The one client-side rule this screen checks before the request rather
  // than after it: a NOT NULL column left empty. Checked entirely offline —
  // the assertion below is that nothing was sent, not just that a message
  // appeared.
  test("refuses to submit a row that leaves a required column empty, without asking the server", async () => {
    gameTableDataWindowAction.mockResolvedValueOnce({
      value: { fromRow: 1, rows: [], totalRows: 0, truncated: false },
    });

    show();
    await screen.findByText(td.emptyRows);

    await userEvent.click(screen.getByRole("button", { name: td.addRowButton }));

    expect(screen.getByText(en.errors.game_table_value_invalid)).toBeInTheDocument();
    expect(appendTableRowAction).not.toHaveBeenCalled();
  });

  test("deletes a row after confirming, and reloads the page it was on", async () => {
    gameTableDataWindowAction.mockResolvedValueOnce({
      value: { fromRow: 1, rows: [{ row: 4, fields: ["Ada", "37"] }], totalRows: 1, truncated: false },
    });
    deleteTableRowAction.mockResolvedValueOnce({});
    gameTableDataWindowAction.mockResolvedValueOnce({
      value: { fromRow: 1, rows: [], totalRows: 0, truncated: false },
    });

    show({ activeRowCount: 1 });
    await screen.findByText("Ada");

    await userEvent.click(screen.getByRole("button", { name: td.deleteRow }));

    expect(window.confirm).toHaveBeenCalledWith(td.deleteRowConfirm.replace("{row}", "4"));
    await waitFor(() => expect(deleteTableRowAction).toHaveBeenCalledWith(contestId, "suspects", 4));
    expect(await screen.findByText(td.emptyRows)).toBeInTheDocument();
  });

  // A tombstone never rewrites Lines (`tabledata.go`'s own `DeleteTableRow`
  // doc: "not a rewrite of the file") — so a delete must only ever move the
  // active count down, and must never touch the structural-lock one, or a
  // deleted row would look like it unlocked a table the server still
  // refuses to let this screen rename or restructure.
  test("deleting a row lowers only the active count, never the total the structure lock reads", async () => {
    gameTableDataWindowAction.mockResolvedValueOnce({
      value: { fromRow: 1, rows: [{ row: 4, fields: ["Ada", "37"] }], totalRows: 5, truncated: false },
    });
    deleteTableRowAction.mockResolvedValueOnce({});
    gameTableDataWindowAction.mockResolvedValueOnce({
      value: { fromRow: 1, rows: [], totalRows: 5, truncated: false },
    });

    const onRowCountChange = vi.fn();
    const onActiveRowCountChange = vi.fn();
    show({ activeRowCount: 3, onRowCountChange, onActiveRowCountChange });
    await screen.findByText("Ada");

    await userEvent.click(screen.getByRole("button", { name: td.deleteRow }));

    await waitFor(() => expect(onActiveRowCountChange).toHaveBeenCalledWith(2));
    // The reload this delete triggers reports the file's own total again
    // (5, unchanged — harmless), but nothing about the delete itself may
    // ever report 4: the old, single-variable decrement this replaces did
    // exactly that, unlocking a table the server still refuses to let this
    // screen restructure.
    expect(onRowCountChange).not.toHaveBeenCalledWith(4);
  });

  // The sequence this whole feature exists to close: the definition is saved,
  // the background job builds the game empty, and only then can a row be
  // typed at all. So every row is written to a game that is already built,
  // and the notice offering to build it again lives in a tree `page.tsx`
  // rendered on the server before the first row existed. Nothing but a
  // refresh brings it back — and without one an organiser fills a table,
  // closes the tab, and is never told the rows never reached a database.
  //
  // Asserted on all three writes that mark the game out of date on the server
  // (AppendTableRow, DeleteTableRow, CompleteTableUpload — tabledata.go's own
  // three), because a refresh missing from any one of them leaves the same
  // silence.
  test("brings the page back from the server after a row is added, so the build notice can appear", async () => {
    appendTableRowAction.mockResolvedValueOnce({ value: tableData({ status: "complete", activeRows: 1 }) });

    show();
    await screen.findByText(td.emptyRows);

    await userEvent.type(screen.getByLabelText("full_name"), "Ada");
    await userEvent.click(screen.getByRole("button", { name: td.addRowButton }));

    await waitFor(() => expect(appendTableRowAction).toHaveBeenCalled());
    await waitFor(() => expect(refresh).toHaveBeenCalled());
  });

  test("brings the page back from the server after a row is deleted", async () => {
    gameTableDataWindowAction.mockResolvedValueOnce({
      value: { fromRow: 1, rows: [{ row: 4, fields: ["Ada", "37"] }], totalRows: 1, truncated: false },
    });
    deleteTableRowAction.mockResolvedValueOnce({});

    show({ activeRowCount: 1 });
    await screen.findByText("Ada");

    await userEvent.click(screen.getByRole("button", { name: td.deleteRow }));

    await waitFor(() => expect(deleteTableRowAction).toHaveBeenCalled());
    await waitFor(() => expect(refresh).toHaveBeenCalled());
  });

  test("brings the page back from the server after a CSV upload completes", async () => {
    beginTableUploadAction.mockResolvedValueOnce({ value: tableData({ declaredBytes: 4 }) });
    request.mockResolvedValueOnce({ received_bytes: 4 });
    completeTableUploadAction.mockResolvedValueOnce({
      value: tableData({ status: "complete", lines: 1, activeRows: 1 }),
    });

    show();
    await screen.findByText(td.emptyRows);

    await userEvent.upload(
      screen.getByLabelText(td.pick),
      new File(["a,b\n"], "rows.csv", { type: "text/csv" }),
    );

    await waitFor(() => expect(completeTableUploadAction).toHaveBeenCalled());
    await waitFor(() => expect(refresh).toHaveBeenCalled());
  });
});
