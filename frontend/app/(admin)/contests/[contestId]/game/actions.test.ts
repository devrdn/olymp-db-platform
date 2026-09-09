import { beforeEach, describe, expect, test, vi } from "vitest";

// `vi.mock` factories are hoisted above every import in this file, so the
// mocks they return have to be built through `vi.hoisted` rather than closed
// over plain top-level `const`s — those would not exist yet when the factory
// actually runs.
const { revalidatePath, serverRequest } = vi.hoisted(() => ({
  revalidatePath: vi.fn(),
  serverRequest: vi.fn(),
}));
vi.mock("next/cache", () => ({ revalidatePath }));
vi.mock("@/lib/api/server", () => ({ serverRequest }));

import { ApiError } from "@/lib/api/client";

import {
  abortGameUploadAction,
  abortTableUploadAction,
  appendTableRowAction,
  beginGameUploadAction,
  beginTableUploadAction,
  completeGameUploadAction,
  completeTableUploadAction,
  currentGameUploadAction,
  deleteTableRowAction,
  gameStatusAction,
  gameTableDataWindowAction,
  gameUploadWindowAction,
  saveGameDefinitionAction,
  saveGameScriptAction,
} from "./actions";

const contestId = "11111111-1111-1111-1111-111111111111";
const uploadId = "22222222-2222-2222-2222-222222222222";

const limits = { enabled: true, chunk_bytes: 8388608, max_file_bytes: 4294967296 };

const game = {
  status: "pending",
  version: 1,
  database: "",
  build_error: "",
  script_bytes: 0,
  building: false,
  updated_at: "2026-03-01T09:00:00Z",
  upload_limits: limits,
};

const dataId = "33333333-3333-3333-3333-333333333333";

const builderLimits = {
  enabled: true,
  chunk_bytes: 4194304,
  max_file_bytes: 1073741824,
  max_tables: 50,
  max_table_columns: 50,
  max_definition_bytes: 65536,
  max_field_bytes: 65536,
  max_line_bytes: 4194304,
  max_rows: 200000,
  max_deleted_rows: 10000,
  column_types: ["integer", "text", "date", "timestamp", "numeric", "boolean"],
};

const tableData = {
  id: dataId,
  table: "suspects",
  declared_bytes: 100,
  received_bytes: 0,
  lines: 0,
  active_rows: 0,
  deleted_rows: [],
  status: "receiving",
  created_at: "2026-03-01T09:00:00Z",
  updated_at: "2026-03-01T09:00:00Z",
  builder_limits: builderLimits,
};

const upload = {
  id: uploadId,
  filename: "dump.sql",
  declared_bytes: 3_000_000_000,
  received_bytes: 1_000_000_000,
  sha256: "",
  lines: 0,
  status: "receiving",
  created_at: "2026-03-01T09:00:00Z",
  updated_at: "2026-03-01T09:05:00Z",
  upload_limits: limits,
};

function form(fields: Record<string, string>): FormData {
  const data = new FormData();
  for (const [key, value] of Object.entries(fields)) data.set(key, value);
  return data;
}

beforeEach(() => {
  serverRequest.mockReset();
  revalidatePath.mockReset();
});

describe("saveGameScriptAction", () => {
  test("stores the script and revalidates, without trimming leading whitespace", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    const state = await saveGameScriptAction({}, form({ contestId, script: "  SELECT 1" }));

    expect(state).toEqual({ saved: true });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/game/script`, {
      method: "PUT",
      body: { script: "  SELECT 1" },
    });
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });

  test("never sends a request for a contest segment that is not an identifier", async () => {
    const state = await saveGameScriptAction({}, form({ contestId: "nope", script: "SELECT 1" }));

    expect(state).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("carries the server's own refusal code back to the form", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("game_script_empty", 400, "empty"));

    const state = await saveGameScriptAction({}, form({ contestId, script: "" }));

    expect(state).toEqual({ code: "game_script_empty" });
  });
});

describe("gameStatusAction", () => {
  test("reads the game's current status", async () => {
    serverRequest.mockResolvedValueOnce(game);

    const result = await gameStatusAction(contestId);

    expect(result).toMatchObject({ status: "pending", building: false });
  });

  test("returns null rather than throwing when the API cannot be reached", async () => {
    serverRequest.mockRejectedValueOnce(new Error("connect ECONNREFUSED"));

    expect(await gameStatusAction(contestId)).toBeNull();
  });

  test("returns null for a contest segment that is not an identifier", async () => {
    expect(await gameStatusAction("nope")).toBeNull();
    expect(serverRequest).not.toHaveBeenCalled();
  });
});

describe("beginGameUploadAction", () => {
  test("reserves a place for a new upload and revalidates", async () => {
    serverRequest.mockResolvedValueOnce({ ...upload, received_bytes: 0, status: "receiving" });

    const result = await beginGameUploadAction(contestId, "dump.sql", 3_000_000_000);

    expect(result.code).toBeUndefined();
    expect(result.value).toMatchObject({ id: uploadId, filename: "dump.sql", declaredBytes: 3_000_000_000 });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/game/uploads`, {
      method: "POST",
      body: { filename: "dump.sql", declared_bytes: 3_000_000_000 },
    });
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });

  test("carries the server's own refusal — a second upload already receiving", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("game_upload_in_progress", 409, "already receiving"));

    const result = await beginGameUploadAction(contestId, "dump.sql", 10);

    expect(result).toEqual({ code: "game_upload_in_progress" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });

  test("never sends a request for a contest segment that is not an identifier", async () => {
    const result = await beginGameUploadAction("nope", "dump.sql", 10);

    expect(result).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});

describe("currentGameUploadAction", () => {
  test("reads the upload still receiving, if any", async () => {
    serverRequest.mockResolvedValueOnce(upload);

    const result = await currentGameUploadAction(contestId);

    expect(result).toMatchObject({ id: uploadId, receivedBytes: 1_000_000_000, status: "receiving" });
  });

  // The handler's own sentinel for "nothing in progress" — not a 404, and
  // not an error this action has anything to report.
  test("reads the absent sentinel as an ordinary upload, not a failure", async () => {
    serverRequest.mockResolvedValueOnce({ ...upload, status: "absent", received_bytes: 0 });

    const result = await currentGameUploadAction(contestId);

    expect(result?.status).toBe("absent");
  });

  test("returns null rather than throwing when the API cannot be reached", async () => {
    serverRequest.mockRejectedValueOnce(new Error("connect ECONNREFUSED"));

    expect(await currentGameUploadAction(contestId)).toBeNull();
  });
});

describe("gameUploadWindowAction", () => {
  test("reads a slice of the completed upload's lines", async () => {
    serverRequest.mockResolvedValueOnce({
      from_line: 1,
      lines: ["CREATE TABLE guests (id uuid);"],
      total_lines: 500,
      truncated: false,
    });

    const result = await gameUploadWindowAction(contestId, uploadId, 1);

    expect(result.value).toEqual({
      fromLine: 1,
      lines: ["CREATE TABLE guests (id uuid);"],
      totalLines: 500,
      truncated: false,
    });
    expect(serverRequest).toHaveBeenCalledWith(
      `/contests/${contestId}/game/uploads/${uploadId}/window?from=1`,
    );
  });

  test("carries the server's own refusal — the upload has not finished yet", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("game_upload_incomplete", 409, "not finished"));

    const result = await gameUploadWindowAction(contestId, uploadId, 1);

    expect(result).toEqual({ code: "game_upload_incomplete" });
  });

  test("never sends a request for an upload segment that is not an identifier", async () => {
    const result = await gameUploadWindowAction(contestId, "nope", 1);

    expect(result).toEqual({ code: "game_upload_not_found" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});

describe("completeGameUploadAction", () => {
  test("finishes the upload and revalidates, the same path SetScript does", async () => {
    serverRequest.mockResolvedValueOnce({ ...game, status: "pending" });

    const result = await completeGameUploadAction(contestId, uploadId);

    expect(result.value).toMatchObject({ status: "pending" });
    expect(serverRequest).toHaveBeenCalledWith(
      `/contests/${contestId}/game/uploads/${uploadId}/complete`,
      { method: "POST" },
    );
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });

  test("carries the server's own refusal — fewer bytes arrived than declared", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("game_upload_length_mismatch", 409, "short"));

    const result = await completeGameUploadAction(contestId, uploadId);

    expect(result).toEqual({ code: "game_upload_length_mismatch" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });
});

describe("abortGameUploadAction", () => {
  test("cancels the upload and revalidates", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    const result = await abortGameUploadAction(contestId, uploadId);

    expect(result).toEqual({});
    expect(serverRequest).toHaveBeenCalledWith(
      `/contests/${contestId}/game/uploads/${uploadId}/abort`,
      { method: "POST" },
    );
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });

  test("carries the server's own refusal — already finished or cancelled", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("game_upload_already_complete", 409, "done"));

    const result = await abortGameUploadAction(contestId, uploadId);

    expect(result).toEqual({ code: "game_upload_already_complete" });
    expect(revalidatePath).not.toHaveBeenCalled();
  });

  test("reports an unreachable API as such rather than as a refusal", async () => {
    serverRequest.mockRejectedValueOnce(new Error("connect ECONNREFUSED"));

    const result = await abortGameUploadAction(contestId, uploadId);

    expect(result).toEqual({ code: "unreachable" });
  });
});

// --- The table builder: a structural description instead of SQL -----------

function definitionForm(contestId: string, definition: unknown): FormData {
  const data = new FormData();
  data.set("contestId", contestId);
  data.set("definition", JSON.stringify(definition));
  return data;
}

describe("saveGameDefinitionAction", () => {
  test("stores the definition whole and revalidates, the same path SetScript does", async () => {
    serverRequest.mockResolvedValueOnce(undefined);
    const definition = { tables: [{ name: "suspects", columns: [{ name: "id", type: "integer" }] }] };

    const state = await saveGameDefinitionAction({}, definitionForm(contestId, definition));

    expect(state).toEqual({ saved: true });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/game/definition`, {
      method: "PUT",
      body: definition,
    });
    expect(revalidatePath).toHaveBeenCalledWith(`/contests/${contestId}`, "layout");
  });

  test("never sends a request for a contest segment that is not an identifier", async () => {
    const state = await saveGameDefinitionAction({}, definitionForm("nope", { tables: [] }));

    expect(state).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });

  test("carries the server's own refusal code, and its own detail, back to the form", async () => {
    serverRequest.mockRejectedValueOnce(
      new ApiError("game_definition_duplicate_name", 400, 'table "Suspects" named twice'),
    );

    const state = await saveGameDefinitionAction({}, definitionForm(contestId, { tables: [] }));

    expect(state).toEqual({
      code: "game_definition_duplicate_name",
      detail: 'table "Suspects" named twice',
    });
  });
});

describe("beginTableUploadAction", () => {
  test("reserves a place for one table's own chunked upload", async () => {
    serverRequest.mockResolvedValueOnce(tableData);

    const result = await beginTableUploadAction(contestId, "suspects", 100);

    expect(result.code).toBeUndefined();
    expect(result.value).toMatchObject({ id: dataId, table: "suspects", declaredBytes: 100 });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/game/tables/suspects/data`, {
      method: "POST",
      body: { declared_bytes: 100 },
    });
  });

  test("encodes a table name that needs it in the path", async () => {
    serverRequest.mockResolvedValueOnce({ ...tableData, table: "a b" });

    await beginTableUploadAction(contestId, "a b", 100);

    expect(serverRequest).toHaveBeenCalledWith(
      `/contests/${contestId}/game/tables/a%20b/data`,
      expect.anything(),
    );
  });

  test("carries the server's own refusal — the table is not part of the current definition", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("game_table_unknown", 404, "no such table"));

    const result = await beginTableUploadAction(contestId, "ghosts", 100);

    expect(result).toEqual({ code: "game_table_unknown" });
  });

  test("never sends a request for a contest segment that is not an identifier", async () => {
    const result = await beginTableUploadAction("nope", "suspects", 100);

    expect(result).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});

describe("completeTableUploadAction", () => {
  test("finishes the table's upload — 200 on the wire, no build to watch", async () => {
    serverRequest.mockResolvedValueOnce({ ...tableData, status: "complete", received_bytes: 100, active_rows: 3 });

    const result = await completeTableUploadAction(contestId, "suspects", dataId);

    expect(result.value).toMatchObject({ status: "complete", activeRows: 3 });
    expect(serverRequest).toHaveBeenCalledWith(
      `/contests/${contestId}/game/tables/suspects/data/${dataId}/complete`,
      { method: "POST" },
    );
  });

  test("carries the server's own refusal — fewer bytes arrived than declared", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("game_table_data_length_mismatch", 409, "short"));

    const result = await completeTableUploadAction(contestId, "suspects", dataId);

    expect(result).toEqual({ code: "game_table_data_length_mismatch", detail: "short" });
  });

  // `validateTableFile`'s own full pass is what actually finds a row whose
  // field count is wrong, and it names the row — this is the detail the
  // brief asks the interface to show rather than a generic "file did not
  // fit" sentence.
  test("carries the row the server named when a file's own data does not fit its columns", async () => {
    serverRequest.mockRejectedValueOnce(
      new ApiError("game_table_row_field_count", 400, "row 12 has 3 field(s), the table has 2 columns"),
    );

    const result = await completeTableUploadAction(contestId, "suspects", dataId);

    expect(result).toEqual({
      code: "game_table_row_field_count",
      detail: "row 12 has 3 field(s), the table has 2 columns",
    });
  });

  test("never sends a request for an upload segment that is not an identifier", async () => {
    const result = await completeTableUploadAction(contestId, "suspects", "nope");

    expect(result).toEqual({ code: "game_table_data_not_found" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});

describe("abortTableUploadAction", () => {
  test("cancels the table's own upload", async () => {
    serverRequest.mockResolvedValueOnce({ ...tableData, status: "aborted" });

    const result = await abortTableUploadAction(contestId, "suspects", dataId);

    expect(result.value).toMatchObject({ status: "aborted" });
    expect(serverRequest).toHaveBeenCalledWith(
      `/contests/${contestId}/game/tables/suspects/data/${dataId}/abort`,
      { method: "POST" },
    );
  });

  test("carries the server's own refusal — already finished or cancelled", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("game_table_data_already_complete", 409, "done"));

    const result = await abortTableUploadAction(contestId, "suspects", dataId);

    expect(result).toEqual({ code: "game_table_data_already_complete" });
  });
});

describe("gameTableDataWindowAction", () => {
  test("reads a page of the table's current rows", async () => {
    serverRequest.mockResolvedValueOnce({
      from_row: 1,
      rows: [{ row: 1, fields: ["Ada", "37"] }],
      total_rows: 12,
      truncated: false,
    });

    const result = await gameTableDataWindowAction(contestId, "suspects", 1);

    expect(result.value).toEqual({
      fromRow: 1,
      rows: [{ row: 1, fields: ["Ada", "37"] }],
      totalRows: 12,
      truncated: false,
    });
    expect(serverRequest).toHaveBeenCalledWith(
      `/contests/${contestId}/game/tables/suspects/data/window?from=1`,
    );
  });

  test("carries the server's own refusal", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("game_table_unknown", 404, "no such table"));

    const result = await gameTableDataWindowAction(contestId, "ghosts", 1);

    expect(result).toEqual({ code: "game_table_unknown" });
  });
});

describe("appendTableRowAction", () => {
  test("adds one row typed into a form", async () => {
    serverRequest.mockResolvedValueOnce({ ...tableData, status: "complete", lines: 1, active_rows: 1 });

    const result = await appendTableRowAction(contestId, "suspects", ["Ada", "37"]);

    expect(result.value).toMatchObject({ activeRows: 1 });
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/game/tables/suspects/rows`, {
      method: "POST",
      body: { values: ["Ada", "37"] },
    });
  });

  // The message names the row and the column at fault — the brief's own
  // requirement — and it travels back to the screen as `detail`
  // (`UploadActionResult`'s own doc, `actions.ts`), never dropped the way a
  // plain `{code}` would drop it.
  test("carries the server's own refusal, naming the row and column at fault", async () => {
    serverRequest.mockRejectedValueOnce(
      new ApiError("game_table_value_invalid", 400, 'row 0, column "age": "old" is not a whole number'),
    );

    const result = await appendTableRowAction(contestId, "suspects", ["Ada", "old"]);

    expect(result).toEqual({
      code: "game_table_value_invalid",
      detail: 'row 0, column "age": "old" is not a whole number',
    });
  });
});

describe("deleteTableRowAction", () => {
  test("tombstones one row", async () => {
    serverRequest.mockResolvedValueOnce(undefined);

    const result = await deleteTableRowAction(contestId, "suspects", 3);

    expect(result).toEqual({});
    expect(serverRequest).toHaveBeenCalledWith(`/contests/${contestId}/game/tables/suspects/rows/3`, {
      method: "DELETE",
    });
  });

  test("carries the server's own refusal — no such row", async () => {
    serverRequest.mockRejectedValueOnce(new ApiError("game_table_row_not_found", 404, "no such row"));

    const result = await deleteTableRowAction(contestId, "suspects", 99);

    expect(result).toEqual({ code: "game_table_row_not_found" });
  });

  test("never sends a request for a contest segment that is not an identifier", async () => {
    const result = await deleteTableRowAction("nope", "suspects", 3);

    expect(result).toEqual({ code: "invalid_contest_id" });
    expect(serverRequest).not.toHaveBeenCalled();
  });
});
