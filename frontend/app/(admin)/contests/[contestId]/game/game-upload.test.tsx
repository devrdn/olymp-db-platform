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

/** An editor-sourced `Game`, the shape most of these tests exercise this
 * panel alongside — `page.tsx` always renders `GameUpload` next to
 * `GameEditor`, whichever of the two actually built the current game. */
function game(overrides: Partial<Game> = {}): Game {
  return {
    status: "absent",
    version: 0,
    database: "",
    source: "editor",
    upload: undefined,
    buildError: "",
    scriptBytes: 0,
    building: false,
    updatedAt: "",
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
  /** Overrides the whole `game` prop — for the file-sourced-reload tests,
   * which need `source`/`upload` alongside a custom `uploadLimits`. */
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
      value: game({ status: "failed", version: 2, buildError: 'line 3: syntax error at or near "FRO"' }),
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

  // Cancel has two halves and they fail separately: the server is told to
  // forget the upload, and the request already on the wire is actually
  // stopped. Only the first was checked here before — the stub ignored the
  // `signal` it was handed, so deleting `controllerRef.current?.abort()` left
  // the test green while a cancelled multi-gigabyte upload went on sending
  // chunks to a contest the organiser had just abandoned. `signal` is in
  // `lib/api/client.ts` for this line alone.
  test("cancels an upload in progress and lets the server know", async () => {
    beginGameUploadAction.mockResolvedValueOnce({ value: upload({ receivedBytes: 0 }) });
    // The chunk stays in flight until its signal says otherwise — the state
    // the loop is in when "Cancel" is pressed, and the only state in which
    // aborting is observable at all.
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

  // This is the reload this whole panel exists to survive: the tab that ran
  // the upload is gone, and `game.upload` (from `GET .../game`, carried in
  // by `page.tsx`) is the only thing left that still names the file. Before
  // this, a reload of a file-sourced game showed the "Choose file" picker as
  // if nothing had ever been uploaded — this proves it instead restores the
  // viewer, not the transient "just finished" message a live completion
  // shows (`sourceNote`, not `done`), and loads the file's own first window
  // on its own, without a click.
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

  // An organiser picks the wrong dump at least once. Before this, the only
  // file picker lived in the idle and resumable states, so a game already
  // built from a file had no way back to one: the panel showed the viewer
  // and nothing else. Replacing is the server's decision to refuse or allow
  // (game_not_editable, exactly as for the editor) — the interface's job is
  // to make the attempt reachable.
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

  // The same reload, but for a build that had already failed before it —
  // `completedGame.buildError` is seeded from `game.upload`'s own sibling
  // field `game.buildError` (`game()`'s default `initialGame` here), and
  // "jump to the failing line" reads it exactly the way it reads a live
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
        buildError: 'line 3: syntax error at or near "FRO"',
        upload: { id: uploadId, filename: "dump.sql", bytes: 4096, lines: 7 },
      }),
    });

    const jump = await screen.findByRole("button", { name: tu.jumpToError });
    await userEvent.click(jump);

    expect(gameUploadWindowAction).toHaveBeenLastCalledWith(contestId, uploadId, 3);
  });
});
