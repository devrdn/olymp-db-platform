"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";

import { buttonVariants } from "@/components/ui/button";
import { Tag } from "@/components/ui/tag";
import { ApiError, failureCode, isAbortError, request } from "@/lib/api/client";
import type { Game, Upload } from "@/lib/api/game";
import { readableBytes, readableDuration } from "@/lib/format/bytes";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import {
  abortGameUploadAction,
  beginGameUploadAction,
  completeGameUploadAction,
  currentGameUploadAction,
  gameUploadWindowAction,
} from "./actions";
import { useGamePoll } from "./game-poll";
import { messageForCode } from "@/lib/i18n/errors";

/**
 * Consecutive out-of-order refusals resynced automatically (this tab's offset
 * fell behind the server's) before asking for Retry, so a broken upload does
 * not spin forever.
 */
const MAX_AUTO_RESYNCS = 3;

type Phase = "idle" | "resumable" | "uploading" | "completing" | "done" | "error";

/**
 * Sends one chunk straight to the API, not through a Server Action: an action's
 * body is capped at 1 MiB while `chunk_bytes` is configured per installation at
 * runtime, and an action buffers its `FormData` where `rawBody: Blob` streams
 * the slice from disk. `request()`'s default empty origin makes it a
 * same-origin `/api` call, forwarded by the reverse proxy or, without one,
 * the `next.config.ts` rewrite.
 */
async function putChunk(
  contestId: string,
  uploadId: string,
  offset: number,
  chunk: Blob,
  signal: AbortSignal,
): Promise<number> {
  const payload = await request(`/contests/${contestId}/game/uploads/${uploadId}/chunk?offset=${offset}`, {
    method: "PUT",
    rawBody: chunk,
    signal,
  });

  const bytes = (payload as { received_bytes?: unknown } | null)?.received_bytes;
  if (typeof bytes !== "number") {
    throw new Error(`malformed chunk response for upload ${uploadId}`);
  }
  return bytes;
}

/**
 * Builds the game from an uploaded dump, on the same screen as `GameEditor` so
 * the shared status stays in view.
 *
 * Progress lives in this component's state; the two panels agree after the
 * build via `router.refresh()`. The viewer for the file the current game was
 * built from must survive a reload, so `game.upload` (present when
 * `game.source` is `"file"`) seeds the "done" phase.
 */
export function GameUpload({
  contestId,
  game,
  initialUpload,
  editable,
  dict,
}: {
  contestId: string;
  /**
   * `game.uploadLimits` holds the ceilings; `game.source` and `game.upload`
   * restore the "done" viewer after a reload.
   */
  game: Game;
  /** The upload still receiving at page load, or null. */
  initialUpload: Upload | null;
  editable: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace.game;
  const tu = t.upload;
  const errors = dict.errors;
  const router = useRouter();
  const uploadLimits = game.uploadLimits;

  const resumable = initialUpload && initialUpload.status === "receiving" ? initialUpload : null;
  // A resumable upload takes priority over the note about the file that built
  // the current game.
  const restored = !resumable && game.source === "file" ? (game.upload ?? null) : null;

  const [phase, setPhase] = useState<Phase>(resumable ? "resumable" : restored ? "done" : "idle");
  const [uploadId, setUploadId] = useState<string | null>(resumable?.id ?? restored?.id ?? null);
  const [filename, setFilename] = useState(resumable?.filename ?? restored?.filename ?? "");
  const [totalBytes, setTotalBytes] = useState(resumable?.declaredBytes ?? restored?.bytes ?? 0);
  const [sentBytes, setSentBytes] = useState(resumable?.receivedBytes ?? restored?.bytes ?? 0);
  const [rateBps, setRateBps] = useState(0);
  /**
   * The last chunk's duration in ms, used as the bar's transition time. A
   * transition longer than the update interval never shows the truth; the token
   * stays the ceiling.
   */
  const [stepMs, setStepMs] = useState<number | null>(null);
  const [mismatch, setMismatch] = useState(false);
  const [errorCode, setErrorCode] = useState<string | null>(null);
  const [completedGame, setCompletedGame] = useState<Game | null>(restored ? game : null);
  // True only after this tab's own upload finished, never for a restored "done"
  // phase; separates "file received" from the note about an older file.
  const [liveCompletion, setLiveCompletion] = useState(false);
  // Mirrors `fileRef.current` for render, since refs must not be read during
  // render.
  const [hasFile, setHasFile] = useState(false);

  const fileRef = useRef<File | null>(null);
  const controllerRef = useRef<AbortController | null>(null);
  const rateOriginRef = useRef<{ time: number; bytes: number }>({ time: 0, bytes: 0 });
  /** When `markProgress` last ran (see `stepMs`). */
  const progressStepRef = useRef(0);

  const [windowFrom, setWindowFrom] = useState(1);
  const [windowLines, setWindowLines] = useState<string[]>([]);
  const [windowTotal, setWindowTotal] = useState(0);
  const [windowTruncated, setWindowTruncated] = useState(false);
  const [windowLoading, setWindowLoading] = useState(false);
  const [windowError, setWindowError] = useState<string | null>(null);
  const [gotoValue, setGotoValue] = useState("1");

  // Polls the build after the upload finishes. Shares `GameEditor`'s timer (see
  // useGamePoll), so the two panels never disagree on the tick a build ends.
  useGamePoll(contestId, phase === "done" && Boolean(completedGame?.building), setCompletedGame);

  // Loads the viewer's first window once per upload id, for both a live
  // completion and a restored one.
  const loadedForRef = useRef<string | null>(null);
  useEffect(() => {
    if (phase !== "done" || !uploadId || loadedForRef.current === uploadId) return;
    loadedForRef.current = uploadId;
    void fetchWindow(uploadId, 1);
    // fetchWindow closes only over setState and contestId.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [phase, uploadId]);

  function markProgress(newSent: number) {
    const now = Date.now();
    const elapsed = (now - rateOriginRef.current.time) / 1000;
    if (elapsed > 0.2) {
      setRateBps((newSent - rateOriginRef.current.bytes) / elapsed);
    }
    // The gap since the previous chunk. A gap across a restart is long and gets
    // clamped to the token, so nothing needs resetting.
    const previous = progressStepRef.current;
    progressStepRef.current = now;
    if (previous > 0) setStepMs(now - previous);
    setSentBytes(newSent);
  }

  /**
   * `id` comes from the caller: `runLoop` calls this before the `setUploadId`
   * update reaches this closure. Click handlers use `loadWindow`.
   */
  async function fetchWindow(id: string, from: number) {
    setWindowLoading(true);
    setWindowError(null);
    const result = await gameUploadWindowAction(contestId, id, Math.max(1, from));
    setWindowLoading(false);
    if (result.code) {
      setWindowError(result.code);
      return;
    }
    const w = result.value;
    if (!w) return;
    setWindowFrom(w.fromLine);
    setWindowLines(w.lines);
    setWindowTotal(w.totalLines);
    setWindowTruncated(w.truncated);
    setGotoValue(String(w.fromLine));
  }

  async function loadWindow(from: number) {
    if (!uploadId) return;
    await fetchWindow(uploadId, from);
  }

  async function runLoop(file: File, id: string, startOffset: number, chunkBytes: number) {
    const controller = new AbortController();
    controllerRef.current = controller;
    rateOriginRef.current = { time: Date.now(), bytes: startOffset };

    let offset = startOffset;
    let resyncs = 0;

    while (offset < file.size) {
      const end = Math.min(offset + chunkBytes, file.size);
      const chunk = file.slice(offset, end);

      try {
        // One at a time: parallel PUTs would race the same offset.
        offset = await putChunk(contestId, id, offset, chunk, controller.signal);
        resyncs = 0;
        markProgress(offset);
      } catch (error) {
        if (isAbortError(error)) return;

        // The server knows the true offset better than this tab, mid-stream as
        // much as on resume.
        if (
          error instanceof ApiError &&
          error.code === "game_upload_chunk_out_of_order" &&
          resyncs < MAX_AUTO_RESYNCS
        ) {
          resyncs += 1;
          const fresh = await currentGameUploadAction(contestId);
          if (fresh && fresh.id === id && fresh.status === "receiving") {
            offset = fresh.receivedBytes;
            markProgress(offset);
            continue;
          }
        }

        setPhase("error");
        setErrorCode(failureCode(error));
        return;
      }
    }

    setPhase("completing");
    const result = await completeGameUploadAction(contestId, id);
    if (result.code) {
      setPhase("error");
      setErrorCode(result.code);
      return;
    }

    setCompletedGame(result.value ?? null);
    setLiveCompletion(true);
    setPhase("done");
    // The `loadedForRef` effect loads the first window; fetching here would
    // race it. The refresh brings `GameEditor`'s server props current, and
    // local state survives it.
    router.refresh();
  }

  async function beginFresh(file: File) {
    setMismatch(false);
    setFilename(file.name);
    setTotalBytes(file.size);
    setSentBytes(0);
    setErrorCode(null);
    setPhase("uploading");

    const result = await beginGameUploadAction(contestId, file.name, file.size);
    const begun = result.value;
    if (result.code || !begun) {
      setPhase("error");
      setErrorCode(result.code ?? "unreachable");
      return;
    }

    fileRef.current = file;
    setHasFile(true);
    setUploadId(begun.id);
    await runLoop(file, begun.id, 0, begun.uploadLimits.chunkBytes);
  }

  async function beginResume(file: File) {
    if (!resumable) return;
    setMismatch(false);
    setErrorCode(null);
    setPhase("uploading");

    // Resend from the server's current count; the one loaded with the page may
    // be stale.
    const fresh = await currentGameUploadAction(contestId);
    const offset =
      fresh && fresh.id === resumable.id && fresh.status === "receiving"
        ? fresh.receivedBytes
        : resumable.receivedBytes;

    fileRef.current = file;
    setHasFile(true);
    setUploadId(resumable.id);
    setFilename(resumable.filename);
    setTotalBytes(resumable.declaredBytes);
    setSentBytes(offset);
    await runLoop(file, resumable.id, offset, (fresh ?? resumable).uploadLimits.chunkBytes);
  }

  function handleFileChange(event: React.ChangeEvent<HTMLInputElement>) {
    const picked = event.target.files?.[0];
    // Cleared at once, so picking the same file again still fires `onChange`.
    event.target.value = "";
    if (!picked) return;

    if (phase === "resumable" && resumable) {
      if (picked.name !== resumable.filename || picked.size !== resumable.declaredBytes) {
        setMismatch(true);
        return;
      }
      void beginResume(picked);
      return;
    }

    void beginFresh(picked);
  }

  async function cancel() {
    if (!window.confirm(tu.cancelConfirm)) return;
    controllerRef.current?.abort();
    const id = uploadId;
    fileRef.current = null;
    setHasFile(false);
    setPhase("idle");
    setUploadId(null);
    setFilename("");
    setTotalBytes(0);
    setSentBytes(0);
    setMismatch(false);
    setErrorCode(null);
    if (id) await abortGameUploadAction(contestId, id);
  }

  /**
   * Back to the picker to replace the file. Not a cancel: the server's upload
   * is complete and stays until replaced. The server refuses once the contest
   * has started.
   */
  function chooseAnother() {
    fileRef.current = null;
    setHasFile(false);
    setPhase("idle");
    setUploadId(null);
    setFilename("");
    setTotalBytes(0);
    setSentBytes(0);
    setMismatch(false);
    setErrorCode(null);
    setWindowFrom(1);
    setWindowLines([]);
    setWindowTotal(0);
    setWindowTruncated(false);
  }

  async function retry() {
    const file = fileRef.current;
    const id = uploadId;
    if (!file || !id) return;

    setErrorCode(null);
    setPhase("uploading");

    const fresh = await currentGameUploadAction(contestId);
    const offset = fresh && fresh.id === id && fresh.status === "receiving" ? fresh.receivedBytes : sentBytes;
    await runLoop(file, id, offset, (fresh?.uploadLimits ?? uploadLimits).chunkBytes);
  }

  function message(code: string): string {
    return messageForCode(code, errors);
  }

  if (!editable) {
    return (
      <section className="flex flex-col gap-3 border-t border-line pt-8">
        <h3 className="text-h3 text-ink">{tu.heading}</h3>
        <p className="text-small text-ink-2">{t.frozen}</p>
      </section>
    );
  }

  if (!uploadLimits.enabled) {
    return (
      <section className="flex flex-col gap-3 border-t border-line pt-8">
        <h3 className="text-h3 text-ink">{tu.heading}</h3>
        <p className="max-w-body text-small text-ink-2">{errors.game_uploads_disabled}</p>
      </section>
    );
  }

  const percent = totalBytes > 0 ? Math.min(100, Math.floor((sentBytes / totalBytes) * 100)) : 0;
  const remaining = totalBytes - sentBytes;
  const etaSeconds = rateBps > 0 ? remaining / rateBps : null;

  const errorLine = (() => {
    const text = completedGame?.buildError ?? "";
    const found = /^line (\d+):/i.exec(text);
    return found ? Number(found[1]) : null;
  })();

  return (
    <section className="flex flex-col gap-4 border-t border-line pt-8">
      <div className="flex flex-col gap-1.5">
        <h3 className="text-h3 text-ink">{tu.heading}</h3>
        <p className="max-w-body text-small text-ink-2">{tu.lede}</p>
      </div>

      {phase === "idle" ? (
        <div className="flex flex-wrap items-center gap-3">
          <label className={cn(buttonVariants({ variant: "secondary" }), "cursor-pointer")}>
            {tu.pick}
            <input type="file" className="sr-only" onChange={handleFileChange} />
          </label>
          <span className="text-label text-ink-3">
            {tu.limitHint.replace("{max}", readableBytes(uploadLimits.maxFileBytes))}
          </span>
        </div>
      ) : null}

      {phase === "resumable" && resumable ? (
        <div className="flex flex-col gap-2 border-l-2 border-warn pl-4">
          <p className="text-control text-ink">{tu.resumeHeading}</p>
          <p className="max-w-body text-small text-ink-2">
            {tu.resumeBody
              .replace("{filename}", resumable.filename)
              .replace("{received}", readableBytes(resumable.receivedBytes))
              .replace("{total}", readableBytes(resumable.declaredBytes))}
          </p>
          {mismatch ? (
            <p role="alert" className="max-w-body text-small text-bad">
              {tu.resumeMismatch.replace("{filename}", resumable.filename)}
            </p>
          ) : null}
          <div className="flex flex-wrap items-center gap-3">
            <label className={cn(buttonVariants({ variant: "secondary" }), "cursor-pointer")}>
              {tu.resumePick}
              <input type="file" className="sr-only" onChange={handleFileChange} />
            </label>
            <button
              type="button"
              onClick={() => void cancel()}
              className={cn(buttonVariants({ variant: "quiet" }))}
            >
              {tu.resumeCancel}
            </button>
          </div>
        </div>
      ) : null}

      {phase === "uploading" || phase === "completing" ? (
        <div className="flex flex-col gap-2.5">
          <div className="flex items-center justify-between gap-3">
            <span className="truncate font-mono text-data text-ink">{filename}</span>
            <Tag tone="warn">{phase === "completing" ? tu.completing : tu.uploading}</Tag>
          </div>
          <div
            role="progressbar"
            aria-valuenow={percent}
            aria-valuemin={0}
            aria-valuemax={100}
            className="h-2 w-full overflow-hidden rounded-full bg-sunk"
          >
            {/* `scaleX`, not `width`: a transform is composited and causes no
               layout (SPEC §6). The track carries the rounding. Duration is
               `min(--t-input, gap since the last chunk)`, so the token, 1ms
               under reduced motion, stays the ceiling. */}
            <div
              className="h-full origin-left bg-accent transition-transform ease-standard"
              style={{
                transform: `scaleX(${percent / 100})`,
                transitionDuration:
                  stepMs === null ? "var(--t-input)" : `min(var(--t-input), ${stepMs}ms)`,
              }}
            />
          </div>
          <div className="flex flex-wrap items-center gap-3 text-label text-ink-3">
            <span>
              {tu.progress
                .replace("{sent}", readableBytes(sentBytes))
                .replace("{total}", readableBytes(totalBytes))
                .replace("{percent}", String(percent))}
            </span>
            {rateBps > 0 ? <span>{tu.rate.replace("{rate}", readableBytes(rateBps))}</span> : null}
            {etaSeconds !== null ? (
              <span>{tu.eta.replace("{time}", readableDuration(etaSeconds))}</span>
            ) : null}
          </div>
          {phase === "uploading" ? (
            <div>
              <button
                type="button"
                onClick={() => void cancel()}
                className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
              >
                {tu.cancel}
              </button>
            </div>
          ) : null}
        </div>
      ) : null}

      {phase === "error" && errorCode ? (
        <div className="flex flex-col gap-2 border-l-2 border-bad pl-4">
          <p role="alert" className="max-w-body text-small text-bad">
            {message(errorCode)}
          </p>
          <div className="flex flex-wrap items-center gap-3">
            {hasFile && uploadId ? (
              <button
                type="button"
                onClick={() => void retry()}
                className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
              >
                {tu.retry}
              </button>
            ) : null}
            <button
              type="button"
              onClick={() => void cancel()}
              className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
            >
              {tu.resumeCancel}
            </button>
          </div>
        </div>
      ) : null}

      {phase === "done" ? (
        <div className="flex flex-col gap-4">
          <div className="flex flex-wrap items-center gap-3">
            <span className="font-mono text-data text-ink">{filename}</span>
            {completedGame ? (
              <Tag
                tone={
                  completedGame.status === "ready"
                    ? "good"
                    : completedGame.status === "failed"
                      ? "bad"
                      : completedGame.building
                        ? "warn"
                        : "mute"
                }
              >
                {t.status[completedGame.status]}
              </Tag>
            ) : null}
            {liveCompletion ? (
              <span className="text-small text-good">{tu.done}</span>
            ) : (
              <span className="text-small text-ink-2">{tu.sourceNote}</span>
            )}
            <button
              type="button"
              onClick={chooseAnother}
              className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
            >
              {tu.replace}
            </button>
          </div>

          <div className="flex flex-col gap-2.5">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <h4 className="text-control text-ink">{tu.viewHeading}</h4>
              {errorLine !== null ? (
                <button
                  type="button"
                  onClick={() => void loadWindow(errorLine)}
                  className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
                >
                  {tu.jumpToError}
                </button>
              ) : null}
            </div>

            <div className="flex flex-col gap-2.5">
              <form
                onSubmit={(event) => {
                  event.preventDefault();
                  const line = Number(gotoValue);
                  if (Number.isFinite(line) && line >= 1) void loadWindow(Math.floor(line));
                }}
                className="flex flex-wrap items-center gap-2"
              >
                <label className="text-label text-ink-3" htmlFor="game-upload-goto">
                  {tu.gotoLabel}
                </label>
                <input
                  id="game-upload-goto"
                  type="number"
                  min={1}
                  value={gotoValue}
                  onChange={(event) => setGotoValue(event.target.value)}
                  className="h-7 w-24 border border-line-2 bg-transparent px-2 text-control-sm text-ink outline-none"
                />
                <button type="submit" className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}>
                  {tu.gotoButton}
                </button>
                <button
                  type="button"
                  disabled={windowFrom <= 1}
                  onClick={() => void loadWindow(Math.max(1, windowFrom - (windowLines.length || 1)))}
                  className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
                >
                  {tu.prev}
                </button>
                <button
                  type="button"
                  disabled={windowFrom + windowLines.length - 1 >= windowTotal}
                  onClick={() => void loadWindow(windowFrom + windowLines.length)}
                  className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
                >
                  {tu.next}
                </button>
                {windowTotal > 0 ? (
                  <span className="text-label text-ink-3">{tu.totalLines.replace("{n}", String(windowTotal))}</span>
                ) : null}
              </form>

              {windowError ? (
                <p role="alert" className="text-small text-bad">
                  {/* Falls back to `tu.windowError`, not the generic "something went wrong". */}
                  {(errors as Record<string, string>)[windowError] ?? tu.windowError}
                </p>
              ) : windowLoading ? (
                <p className="text-small text-ink-3">{tu.loading}</p>
              ) : (
                <div className="overflow-x-auto border border-line-2 bg-sunk">
                  <div className="min-w-max">
                    {windowLines.map((line, index) => (
                      <div key={windowFrom + index} className="flex gap-3 px-3 py-0.5 font-mono text-data odd:bg-panel">
                        <span className="w-12 shrink-0 select-none text-right text-ink-3">
                          {windowFrom + index}
                        </span>
                        <span className="whitespace-pre text-ink">{line}</span>
                      </div>
                    ))}
                  </div>
                </div>
              )}
              {windowTruncated ? <p className="text-small text-ink-2">{tu.windowTruncated}</p> : null}
            </div>
          </div>
        </div>
      ) : null}
    </section>
  );
}
