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
  beginGameUploadAction,
  completeGameUploadAction,
  currentGameUploadAction,
  gameStatusAction,
  gameUploadWindowAction,
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
