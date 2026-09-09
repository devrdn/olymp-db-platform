import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { BuilderLimits, TableDefinition } from "@/lib/api/game";

import { GameBuilderTable } from "./game-builder-table";

const beginTableUploadAction = vi.hoisted(() => vi.fn());
const completeTableUploadAction = vi.hoisted(() => vi.fn());
const abortTableUploadAction = vi.hoisted(() => vi.fn());
const gameTableDataWindowAction = vi.hoisted(() => vi.fn());
const appendTableRowAction = vi.hoisted(() => vi.fn());
const deleteTableRowAction = vi.hoisted(() => vi.fn());

vi.mock("./actions", () => ({
  beginTableUploadAction,
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

function tableData(overrides: Record<string, unknown> = {}) {
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
  rowCount = 0,
  onRowCountChange = vi.fn(),
}: {
  active?: boolean;
  editable?: boolean;
  enabled?: boolean;
  rowCount?: number;
  onRowCountChange?: (n: number) => void;
} = {}) {
  return render(
    <GameBuilderTable
      contestId={contestId}
      table={table}
      active={active}
      limits={{ ...limits, enabled }}
      editable={editable}
      rowCount={rowCount}
      onRowCountChange={onRowCountChange}
      dict={en}
    />,
  );
}

beforeEach(() => {
  beginTableUploadAction.mockReset();
  completeTableUploadAction.mockReset();
  abortTableUploadAction.mockReset();
  gameTableDataWindowAction.mockReset();
  appendTableRowAction.mockReset();
  deleteTableRowAction.mockReset();
  request.mockReset();
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

    show({ rowCount: 1 });
    await screen.findByText("Ada");

    await userEvent.click(screen.getByRole("button", { name: td.deleteRow }));

    expect(window.confirm).toHaveBeenCalledWith(td.deleteRowConfirm.replace("{row}", "4"));
    await waitFor(() => expect(deleteTableRowAction).toHaveBeenCalledWith(contestId, "suspects", 4));
    expect(await screen.findByText(td.emptyRows)).toBeInTheDocument();
  });
});
