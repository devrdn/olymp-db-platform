import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { Upload, UploadLimits } from "@/lib/api/game";

import { GameUpload } from "./game-upload";

const refresh = vi.fn();
vi.mock("next/navigation", () => ({ useRouter: () => ({ refresh }) }));

const beginGameUploadAction = vi.hoisted(() => vi.fn());
const currentGameUploadAction = vi.hoisted(() => vi.fn());
const completeGameUploadAction = vi.hoisted(() => vi.fn());
const abortGameUploadAction = vi.hoisted(() => vi.fn());
const gameUploadWindowAction = vi.hoisted(() => vi.fn());
const gameStatusAction = vi.hoisted(() => vi.fn());

vi.mock("./actions", () => ({
  beginGameUploadAction,
  currentGameUploadAction,
  completeGameUploadAction,
  abortGameUploadAction,
  gameUploadWindowAction,
  gameStatusAction,
}));

// `request` (the direct-to-API chunk transport) is the one thing this
// component talks to that is not a Server Action — everything else in
// `@/lib/api/client` (ApiError, the codes) has to stay real, since the
// component's own error handling branches on `instanceof ApiError`.
const request = vi.hoisted(() => vi.fn());
vi.mock("@/lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api/client")>();
  return { ...actual, request };
});

import { ApiError } from "@/lib/api/client";

const contestId = "11111111-1111-1111-1111-111111111111";
const uploadId = "22222222-2222-2222-2222-222222222222";
const t = en.workspace.game;
const tu = t.upload;

const limits: UploadLimits = { enabled: true, chunkBytes: 5, maxFileBytes: 4 * 1024 * 1024 * 1024 };

function upload(overrides: Partial<Upload> = {}): Upload {
  return {
    id: uploadId,
    filename: "dump.sql",
    declaredBytes: 12,
    receivedBytes: 0,
    sha256: "",
    lines: 0,
    status: "receiving",
    createdAt: "2026-03-01T09:00:00Z",
    updatedAt: "2026-03-01T09:00:00Z",
    uploadLimits: limits,
    ...overrides,
  };
}

function show({
  uploadLimits = limits,
  initialUpload = null,
  editable = true,
}: { uploadLimits?: UploadLimits; initialUpload?: Upload | null; editable?: boolean } = {}) {
  return render(
    <GameUpload
      contestId={contestId}
      uploadLimits={uploadLimits}
      initialUpload={initialUpload}
      editable={editable}
      dict={en}
    />,
  );
}

beforeEach(() => {
  refresh.mockClear();
  beginGameUploadAction.mockReset();
  currentGameUploadAction.mockReset();
  completeGameUploadAction.mockReset();
  abortGameUploadAction.mockReset();
  gameUploadWindowAction.mockReset();
  gameStatusAction.mockReset();
  request.mockReset();
  vi.spyOn(window, "confirm").mockReturnValue(true);
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the game upload panel", () => {
  test("says uploads are off rather than offering a picker nobody can use", () => {
    show({ uploadLimits: { enabled: false, chunkBytes: 0, maxFileBytes: 0 } });

    expect(screen.getByText(en.errors.game_uploads_disabled)).toBeInTheDocument();
    expect(screen.queryByText(tu.pick)).not.toBeInTheDocument();
  });

  test("says the game can no longer be replaced once the contest is running", () => {
    show({ editable: false });

    expect(screen.getByText(t.frozen)).toBeInTheDocument();
    expect(screen.queryByText(tu.pick)).not.toBeInTheDocument();
  });

  test("offers a picker with this installation's own size ceiling, and nothing else, when idle", () => {
    show();

    expect(screen.getByLabelText(tu.pick)).toBeInTheDocument();
    expect(screen.getByText(tu.limitHint.replace("{max}", "4.0 GiB"))).toBeInTheDocument();
  });

  test("shows an unfinished upload and asks for the same file to continue", () => {
    show({ initialUpload: upload({ receivedBytes: 5 }) });

    expect(screen.getByText(tu.resumeHeading)).toBeInTheDocument();
    expect(
      screen.getByText(
        tu.resumeBody.replace("{filename}", "dump.sql").replace("{received}", "5 B").replace("{total}", "12 B"),
      ),
    ).toBeInTheDocument();
  });

  // The mistake this guards against is resuming with a file that only looks
  // right — a same-named file that was edited since, or the wrong contest's
  // dump entirely.
  test("refuses to resume with a file that does not match the unfinished upload", async () => {
    show({ initialUpload: upload({ receivedBytes: 5 }) });
    const wrongFile = new File([new Uint8Array(3)], "other.sql");

    await userEvent.upload(screen.getByLabelText(tu.resumePick), wrongFile);

    expect(screen.getByText(tu.resumeMismatch.replace("{filename}", "dump.sql"))).toBeInTheDocument();
    expect(beginGameUploadAction).not.toHaveBeenCalled();
    expect(currentGameUploadAction).not.toHaveBeenCalled();
  });

  test("sends a small file in chunks sized from upload_limits, then shows what was received", async () => {
    beginGameUploadAction.mockResolvedValueOnce({ value: upload({ receivedBytes: 0 }) });
    // A real server, not a canned sequence: it reports back exactly as many
    // bytes as this piece actually carried. A chunk sliced to any size other
    // than upload_limits.chunkBytes (5) still finishes the 12-byte file, but
    // not in three pieces of 5, 5 and 2 — which is what the assertions below
    // check, catching a chunk size taken from a constant instead of the
    // server's own ceiling.
    let received = 0;
    request.mockImplementation(async (_path: string, options: { rawBody?: Blob }) => {
      received += options.rawBody?.size ?? 0;
      return { received_bytes: received };
    });
    completeGameUploadAction.mockResolvedValueOnce({
      value: { status: "pending", version: 2, database: "", buildError: "", scriptBytes: 0, building: true, updatedAt: "", uploadLimits: limits },
    });
    gameUploadWindowAction.mockResolvedValueOnce({
      value: { fromLine: 1, lines: ["CREATE TABLE guests (id uuid);"], totalLines: 1, truncated: false },
    });

    show();
    const file = new File([new Uint8Array(12)], "dump.sql");

    await userEvent.upload(screen.getByLabelText(tu.pick), file);

    await waitFor(() => expect(screen.getByText(tu.done)).toBeInTheDocument());

    expect(beginGameUploadAction).toHaveBeenCalledWith(contestId, "dump.sql", 12);
    expect(request).toHaveBeenCalledTimes(3);
    const [urls, sizes] = [
      request.mock.calls.map((call) => call[0]),
      request.mock.calls.map((call) => (call[1] as { rawBody?: Blob }).rawBody?.size),
    ];
    expect(urls).toEqual([
      `/contests/${contestId}/game/uploads/${uploadId}/chunk?offset=0`,
      `/contests/${contestId}/game/uploads/${uploadId}/chunk?offset=5`,
      `/contests/${contestId}/game/uploads/${uploadId}/chunk?offset=10`,
    ]);
    expect(sizes).toEqual([5, 5, 2]);
    expect(completeGameUploadAction).toHaveBeenCalledWith(contestId, uploadId);
    expect(screen.getByText("CREATE TABLE guests (id uuid);")).toBeInTheDocument();
    expect(refresh).toHaveBeenCalled();
  });

  // The brief's own rule: the next chunk's offset is the server's own
  // received_bytes, not this tab's tally of what it has sent — the two can
  // disagree (a slow write that landed more than this chunk's own length,
  // a resend the server treated as an idempotent no-op), and the server is
  // the one that actually knows.
  test("resumes the next chunk from the server's own received_bytes, not from what this tab sent", async () => {
    beginGameUploadAction.mockResolvedValueOnce({ value: upload({ receivedBytes: 0, declaredBytes: 12 }) });
    // The first chunk is 5 bytes long, but the server reports 7 received —
    // a bigger jump than this chunk's own length would explain.
    request
      .mockResolvedValueOnce({ received_bytes: 7 })
      .mockResolvedValueOnce({ received_bytes: 12 });
    completeGameUploadAction.mockResolvedValueOnce({ code: "unreachable" });

    show();
    const file = new File([new Uint8Array(12)], "dump.sql");
    await userEvent.upload(screen.getByLabelText(tu.pick), file);

    await waitFor(() => expect(request).toHaveBeenCalledTimes(2));

    expect(request).toHaveBeenNthCalledWith(
      2,
      `/contests/${contestId}/game/uploads/${uploadId}/chunk?offset=7`,
      expect.objectContaining({ method: "PUT" }),
    );
  });

  test("shows the server's own refusal when a chunk is rejected, and offers to retry", async () => {
    beginGameUploadAction.mockResolvedValueOnce({ value: upload({ receivedBytes: 0 }) });
    request.mockRejectedValueOnce(new ApiError("game_upload_too_large", 400, "too big"));

    show();
    const file = new File([new Uint8Array(12)], "dump.sql");
    await userEvent.upload(screen.getByLabelText(tu.pick), file);

    await waitFor(() => expect(screen.getByText(en.errors.game_upload_too_large)).toBeInTheDocument());
    expect(screen.getByRole("button", { name: tu.retry })).toBeInTheDocument();
  });

  // The line the build failure names is exactly what a person opening this
  // panel wants to jump to — it is only offered once there is one to jump to.
  test("offers to jump to the failing line once the upload's own build has failed", async () => {
    beginGameUploadAction.mockResolvedValueOnce({ value: upload({ receivedBytes: 0, declaredBytes: 5 }) });
    request.mockResolvedValueOnce({ received_bytes: 5 });
    completeGameUploadAction.mockResolvedValueOnce({
      value: {
        status: "failed",
        version: 2,
        database: "",
        buildError: 'line 3: syntax error at or near "FRO"',
        scriptBytes: 0,
        building: false,
        updatedAt: "",
        uploadLimits: limits,
      },
    });
    gameUploadWindowAction
      .mockResolvedValueOnce({
        value: { fromLine: 1, lines: ["SELEC"], totalLines: 5, truncated: false },
      })
      .mockResolvedValueOnce({
        value: { fromLine: 3, lines: ['FRO "guests"'], totalLines: 5, truncated: false },
      });

    show();
    const file = new File([new Uint8Array(5)], "dump.sql");
    await userEvent.upload(screen.getByLabelText(tu.pick), file);

    const jump = await screen.findByRole("button", { name: tu.jumpToError });
    await userEvent.click(jump);

    expect(gameUploadWindowAction).toHaveBeenLastCalledWith(contestId, uploadId, 3);
  });

  test("cancels an upload in progress and lets the server know", async () => {
    beginGameUploadAction.mockResolvedValueOnce({ value: upload({ receivedBytes: 0 }) });
    // Never resolves: the loop is left waiting on its first chunk, exactly
    // where "Cancel" has to be able to reach it.
    request.mockReturnValueOnce(new Promise(() => {}));
    abortGameUploadAction.mockResolvedValueOnce({});

    show();
    const file = new File([new Uint8Array(12)], "dump.sql");
    await userEvent.upload(screen.getByLabelText(tu.pick), file);

    const cancelButton = await screen.findByRole("button", { name: tu.cancel });
    await userEvent.click(cancelButton);

    expect(window.confirm).toHaveBeenCalledWith(tu.cancelConfirm);
    await waitFor(() => expect(abortGameUploadAction).toHaveBeenCalledWith(contestId, uploadId));
    expect(screen.getByLabelText(tu.pick)).toBeInTheDocument();
  });
});
