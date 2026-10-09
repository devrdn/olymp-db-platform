import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { BuilderLimits, TableData, TableDefinition } from "@/lib/api/game";

import { GameBuilderTable } from "./game-builder-table";

// `router.refresh()` brings back the server-rendered build notice after a
// write; without it the notice appears only on reload.
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

// Chunks go straight to the API, not through a Server Action.
const request = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api/client")>();
  return { ...actual, request };
});

import { ApiError } from "@/lib/api/client";

const contestId = "11111111-1111-1111-1111-111111111111";
const dataId = "33333333-3333-3333-3333-333333333333";
const td = en.workspace.game.builder.data;

// A chunk size that would not appear by coincidence if a hardcoded number
// replaced `builder_limits.chunk_bytes`.
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
  /** Active row count, as opposed to `Lines` reported through `onRowCountChange`. */
  activeRowCount?: number;
  onActiveRowCountChange?: (n: number) => void;
  /** The upload still receiving at page load, or null. */
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

  // An active tab fetches its first window on mount; an empty page is the
  // default, and `mockResolvedValueOnce` in a test still wins.
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

  // The unfinished upload arrives as a prop read by `page.tsx`; the component
  // does not fetch it on mount.
  test("shows an unfinished upload and asks for a file of the same size to continue", () => {
    show({ initialTableData: tableData({ receivedBytes: 5, declaredBytes: 12 }) });

    expect(screen.getByText(td.resumeHeading)).toBeInTheDocument();
    expect(
      screen.getByText(td.resumeBody.replace("{received}", "5 B").replace("{total}", "12 B")),
    ).toBeInTheDocument();
  });

  // A table upload has no filename, so size is the one check before spending a
  // request.
  test("refuses to resume with a file whose size does not match the unfinished upload", async () => {
    show({ initialTableData: tableData({ receivedBytes: 5, declaredBytes: 12 }) });
    const wrongFile = new File([new Uint8Array(3)], "other.csv");

    await userEvent.upload(screen.getByLabelText(td.resumePick), wrongFile);

    expect(screen.getByText(td.resumeMismatch.replace("{total}", "12 B"))).toBeInTheDocument();
    expect(beginTableUploadAction).not.toHaveBeenCalled();
    expect(currentTableUploadAction).not.toHaveBeenCalled();
  });

  // A resume continues from the server's current count, not the stale one the
  // page loaded with.
  test("resumes an unfinished upload from the server's own current offset, not the one the page loaded with", async () => {
    currentTableUploadAction.mockResolvedValueOnce(tableData({ receivedBytes: 7, declaredBytes: 12 }));
    request.mockResolvedValueOnce({ received_bytes: 12 });
    completeTableUploadAction.mockResolvedValueOnce({ value: tableData({ status: "complete", activeRows: 1 }) });

    // Loaded with 5 received; the server has 7 by now.
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

  // A constant chunk size would finish the file, but not in pieces of 5, 5 and
  // 2.
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

  // The next chunk continues from the server's received_bytes, not this tab's
  // total.
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

  // The header check runs on the chunk PUT, which this component makes
  // directly, so the detail comes off the thrown `ApiError`.
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

  // For a typed row the detail comes back through the Server Action's `detail`.
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

  // `Lines` drives the structure lock (a tombstone leaves the old header) and
  // `ActiveRows()` is displayed; a write reports both, separately.
  test("reports a row's own total and its active count separately, not as one shared number", async () => {
    gameTableDataWindowAction.mockResolvedValueOnce({
      value: { fromRow: 1, rows: [], totalRows: 10, truncated: false },
    });
    // Ten rows, three deleted, one added: 11 lines, 8 active.
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
    // Reported by the write itself, not only by the follow-up window fetch,
    // which would mask a wrong value.
    expect(onRowCountChange).toHaveBeenCalledWith(11);
    expect(onRowCountChange).not.toHaveBeenCalledWith(8);
  });

  // A NOT NULL column left empty is refused offline: nothing is sent.
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

  // A delete moves only the active count; lowering `Lines` would unlock a table
  // the server still locks.
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
    // The follow-up fetch reports 5 again; the delete itself must never report
    // 4.
    expect(onRowCountChange).not.toHaveBeenCalledWith(4);
  });

  // Rows can only be typed after the game is built empty, so the build notice
  // lives in a tree rendered before the first row existed; only a refresh
  // brings it back. Asserted for all three writes that mark the game out of
  // date.
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
