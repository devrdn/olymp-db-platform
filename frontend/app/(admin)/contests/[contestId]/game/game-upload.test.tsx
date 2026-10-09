import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";

import en from "@/lib/i18n/dictionaries/en";
import type { Game, Upload, UploadLimits } from "@/lib/api/game";

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

// Only the chunk transport is mocked; `ApiError` must stay real because the
// component branches on `instanceof`.
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

/**
 * A build failure as `internal/gamedb` writes it: `scriptErrorLinePrefix` plus
 * PostgreSQL's message. The prefix is what the panel parses.
 */
function buildFailureAtLine(line: number): string {
  return `line ${line}: the game script was refused: syntax error at or near "FRO" (SQLSTATE 42601)`;
}

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

/** An editor-sourced `Game`; the page always renders `GameUpload` beside `GameEditor`. */
function game(overrides: Partial<Game> = {}): Game {
  return {
    status: "absent",
    version: 0,
    database: "",
    source: "editor",
    upload: undefined,
    buildError: "",
    scriptBytes: 0,
    maxScriptBytes: 512 * 1024,
    building: false,
    updatedAt: "",
    needsBuild: false,
    uploadLimits: limits,
    ...overrides,
  };
}

function show({
  uploadLimits,
  initialGame,
  initialUpload = null,
  editable = true,
}: {
  uploadLimits?: UploadLimits;
  /** Replaces the whole `game` prop, for the file-sourced reload tests. */
  initialGame?: Game;
  initialUpload?: Upload | null;
  editable?: boolean;
} = {}) {
  return render(
    <GameUpload
      contestId={contestId}
      game={initialGame ?? game({ uploadLimits: uploadLimits ?? limits })}
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

  // Guards against resuming with an edited same-named file or the wrong dump.
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
    // The fake server reports exactly what each piece carried, so a constant
    // chunk size would show up as pieces other than 5, 5 and 2.
    let received = 0;
    request.mockImplementation(async (_path: string, options: { rawBody?: Blob }) => {
      received += options.rawBody?.size ?? 0;
      return { received_bytes: received };
    });
    completeGameUploadAction.mockResolvedValueOnce({
      value: game({ status: "pending", version: 2, building: true }),
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

  // The server's received_bytes wins over this tab's tally; they can differ
  // after an idempotent resend.
  test("resumes the next chunk from the server's own received_bytes, not from what this tab sent", async () => {
    beginGameUploadAction.mockResolvedValueOnce({ value: upload({ receivedBytes: 0, declaredBytes: 12 }) });
    // A 5-byte chunk, but the server reports 7.
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

  // Offered only once a failing line exists. The failure format is shared with
  // Go without a generator, so it is pinned on both sides;
  // `TestBothScriptFailuresNameTheLineInTheShapeTheConsoleParses` runs this
  // component's regex over Go's output.
  test("offers to jump to the failing line once the upload's own build has failed", async () => {
    beginGameUploadAction.mockResolvedValueOnce({ value: upload({ receivedBytes: 0, declaredBytes: 5 }) });
    request.mockResolvedValueOnce({ received_bytes: 5 });
    completeGameUploadAction.mockResolvedValueOnce({
      value: game({ status: "failed", version: 2, buildError: buildFailureAtLine(3) }),
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

  // Cancel must both tell the server and abort the chunk on the wire; a stub
  // ignoring `signal` would let a removed `abort()` pass.
  test("cancels an upload in progress and lets the server know", async () => {
    beginGameUploadAction.mockResolvedValueOnce({ value: upload({ receivedBytes: 0 }) });
    // The chunk stays in flight until aborted, the only state where aborting is
    // observable.
    let chunkSignal: AbortSignal | undefined;
    request.mockImplementationOnce((_path: string, init: { signal: AbortSignal }) => {
      chunkSignal = init.signal;
      return new Promise((_resolve, reject) => {
        init.signal.addEventListener("abort", () =>
          reject(new DOMException("The operation was aborted.", "AbortError")),
        );
      });
    });
    abortGameUploadAction.mockResolvedValueOnce({});

    show();
    const file = new File([new Uint8Array(12)], "dump.sql");
    await userEvent.upload(screen.getByLabelText(tu.pick), file);

    const cancelButton = await screen.findByRole("button", { name: tu.cancel });
    await waitFor(() => expect(chunkSignal).toBeDefined());
    await userEvent.click(cancelButton);

    expect(window.confirm).toHaveBeenCalledWith(tu.cancelConfirm);
    expect(chunkSignal?.aborted).toBe(true);
    await waitFor(() => expect(abortGameUploadAction).toHaveBeenCalledWith(contestId, uploadId));
    expect(screen.getByLabelText(tu.pick)).toBeInTheDocument();
  });

  // After a reload only `game.upload` names the file: the viewer is restored
  // (with `sourceNote`, not the live `done` message) and loads its first window
  // without a click.
  test("restores the file viewer for a file-sourced game after a reload, rather than an empty picker", async () => {
    gameUploadWindowAction.mockResolvedValueOnce({
      value: { fromLine: 1, lines: ["CREATE TABLE guests (id uuid);"], totalLines: 1, truncated: false },
    });

    show({
      initialGame: game({
        status: "ready",
        version: 3,
        source: "file",
        upload: { id: uploadId, filename: "dump.sql", bytes: 4096, lines: 7 },
      }),
    });

    expect(screen.queryByLabelText(tu.pick)).not.toBeInTheDocument();
    expect(screen.getByText("dump.sql")).toBeInTheDocument();
    expect(screen.getByText(tu.sourceNote)).toBeInTheDocument();
    expect(screen.queryByText(tu.done)).not.toBeInTheDocument();

    await waitFor(() =>
      expect(gameUploadWindowAction).toHaveBeenCalledWith(contestId, uploadId, 1),
    );
    expect(await screen.findByText("CREATE TABLE guests (id uuid);")).toBeInTheDocument();
  });

  // A built game must still offer the picker; whether replacing is allowed is
  // the server's call.
  test("offers a way back to the picker so a different file can replace this one", async () => {
    gameUploadWindowAction.mockResolvedValueOnce({
      value: { fromLine: 1, lines: ["CREATE TABLE guests (id uuid);"], totalLines: 1, truncated: false },
    });

    show({
      initialGame: game({
        status: "ready",
        version: 3,
        source: "file",
        upload: { id: uploadId, filename: "dump.sql", bytes: 4096, lines: 7 },
      }),
    });

    expect(screen.queryByLabelText(tu.pick)).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: tu.replace }));

    expect(screen.getByLabelText(tu.pick)).toBeInTheDocument();
    expect(screen.queryByText("dump.sql")).not.toBeInTheDocument();
  });

  // The restored build error comes from `game.buildError`, read like a live
  // completion's.
  test("offers to jump to the failing line for a file-sourced game restored after a reload", async () => {
    gameUploadWindowAction
      .mockResolvedValueOnce({
        value: { fromLine: 1, lines: ["SELEC"], totalLines: 5, truncated: false },
      })
      .mockResolvedValueOnce({
        value: { fromLine: 3, lines: ['FRO "guests"'], totalLines: 5, truncated: false },
      });

    show({
      initialGame: game({
        status: "failed",
        version: 3,
        source: "file",
        buildError: buildFailureAtLine(3),
        upload: { id: uploadId, filename: "dump.sql", bytes: 4096, lines: 7 },
      }),
    });

    const jump = await screen.findByRole("button", { name: tu.jumpToError });
    await userEvent.click(jump);

    expect(gameUploadWindowAction).toHaveBeenLastCalledWith(contestId, uploadId, 3);
  });
});

/**
 * jsdom runs no transitions, so this checks which property moves (a transform,
 * not `width`) and where the duration comes from.
 */
describe("the upload's progress bar", () => {
  /** Holds the first chunk open so the bar stays on screen. */
  function stall() {
    beginGameUploadAction.mockResolvedValueOnce({ value: upload({ receivedBytes: 0, declaredBytes: 12 }) });
    request.mockImplementation(() => new Promise(() => {}));
  }

  test("moves with a transform rather than by re-laying itself out", async () => {
    stall();
    show();

    await userEvent.upload(screen.getByLabelText(tu.pick), new File([new Uint8Array(12)], "dump.sql"));

    const bar = await screen.findByRole("progressbar");
    const fill = bar.firstElementChild as HTMLElement;

    expect(fill.style.transform).toMatch(/^scaleX\(/);
    expect(fill.style.width).toBe("");
    expect(fill.className).toContain("transition-transform");
    expect(fill.className).not.toContain("transition-[width]");
  });

  // Before the first chunk there is no cadence, so the token is the duration.
  test("takes its duration from the token until there is a cadence to measure", async () => {
    stall();
    show();

    await userEvent.upload(screen.getByLabelText(tu.pick), new File([new Uint8Array(12)], "dump.sql"));

    const fill = (await screen.findByRole("progressbar")).firstElementChild as HTMLElement;

    expect(fill.style.transitionDuration).toBe("var(--t-input)");
  });

  test("and never travels for longer than the gap between two chunks", async () => {
    beginGameUploadAction.mockResolvedValueOnce({ value: upload({ receivedBytes: 0, declaredBytes: 12 }) });
    let received = 0;
    let chunks = 0;
    request.mockImplementation(async (_path: string, options: { rawBody?: Blob }) => {
      chunks += 1;
      received += options.rawBody?.size ?? 0;
      // Two chunks land, then the upload hangs with the bar on screen.
      if (chunks > 2) await new Promise(() => {});
      return { received_bytes: received };
    });

    show();
    await userEvent.upload(screen.getByLabelText(tu.pick), new File([new Uint8Array(12)], "dump.sql"));

    await waitFor(() => expect(request).toHaveBeenCalledTimes(3));

    const fill = (await screen.findByRole("progressbar")).firstElementChild as HTMLElement;
    expect(fill.style.transitionDuration).toMatch(/^min\(var\(--t-input\), \d+ms\)$/);
  });
});
