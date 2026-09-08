"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";

import { buttonVariants } from "@/components/ui/button";
import { Tag } from "@/components/ui/tag";
import { ApiError, request } from "@/lib/api/client";
import type { Game, Upload } from "@/lib/api/game";
import { GAME_POLL_MS } from "@/lib/api/game-terms";
import { readableBytes, readableDuration } from "@/lib/format/bytes";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import {
  abortGameUploadAction,
  beginGameUploadAction,
  completeGameUploadAction,
  currentGameUploadAction,
  gameStatusAction,
  gameUploadWindowAction,
} from "./actions";

/** How many consecutive out-of-order refusals the loop resyncs from on its
 * own before giving up and asking a person to press Retry. Covers the one
 * legitimate cause — this tab's own idea of the offset fell behind the
 * server's, the exact gap `game_upload_chunk_out_of_order`'s own text
 * describes — without spinning forever against a genuinely broken upload. */
const MAX_AUTO_RESYNCS = 3;

type Phase = "idle" | "resumable" | "uploading" | "completing" | "done" | "error";

/** True for the DOMException `fetch` rejects an aborted request with. */
function isAbortError(error: unknown): boolean {
  return error instanceof Error && error.name === "AbortError";
}

function failureCode(error: unknown): string {
  return error instanceof ApiError ? error.code : "unreachable";
}

/**
 * One chunk of a game upload, sent straight to the API rather than through a
 * Server Action.
 *
 * Every other write in this screen (`./actions.ts`) goes through one: small
 * JSON, and the API's origin never has to reach the browser. A chunk cannot
 * follow it there for two reasons at once. First, size — a Server Action's
 * request body is capped (1 MiB by default, and `next.config.ts` has no
 * static number to raise it to, because `chunk_bytes` is this *installation's
 * own* configured ceiling, read at runtime from `upload_limits`, not a build-
 * time constant this file could match). Second, memory — a Server Action
 * receives its payload as `FormData` the framework itself buffers before this
 * code ever runs, where `rawBody: Blob` here lets the browser stream the
 * slice off disk. `client.ts`'s `request()` was built framework-free for
 * exactly this: `origin: ""` (its default) is a same-origin relative request,
 * which is what a browser call needs and a Server Action never did — the
 * `next.config.ts` rewrite that already lets the browser fetch a settings
 * image straight from `/api/v1/...` is the same route this reaches.
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
 * The second way to build this contest's game: a finished dump, sent in
 * pieces, instead of a script typed into `GameEditor` above it.
 *
 * A second control on the same screen rather than a tab of its own — an
 * organiser choosing between them is choosing between two ways to produce
 * the very thing `GameEditor`'s own status tag already reports on, and
 * hiding one behind a click would only make that status harder to find.
 *
 * The upload's own progress lives entirely in this component's state, not in
 * a prop `GameEditor` also reads: the two screens agree once the build
 * itself finishes, through `router.refresh()` re-reading `/game` the normal
 * way (`page.tsx`'s own `Promise.all`), which is the same mechanism a plain
 * reload uses and needs no wiring between two otherwise independent forms.
 *
 * The one exception is the viewer for a file this contest's game was already
 * built from: that has to survive a reload, because the tab that ran the
 * upload is gone by then and nothing else on this page still names the file.
 * `game.upload` (`gameResponse.Upload`, present exactly when `game.source`
 * is `"file"`) is what carries that fact across the reload — it is what
 * seeds this component's state back into the "done" phase below, the same
 * phase a live completion (`runLoop`) reaches on its own.
 */
export function GameUpload({
  contestId,
  game,
  initialUpload,
  editable,
  dict,
}: {
  contestId: string;
  /** The game as `page.tsx` read it — `game.uploadLimits` is this panel's
   * own ceilings, and `game.source` / `game.upload` are what let it restore
   * the "done" viewer after a reload (this component's own doc explains
   * why nothing else can). */
  game: Game;
  /** The upload a reloaded page found still receiving, or null. */
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
  // A file-sourced game whose own upload this reload can still describe —
  // never true at the same time as `resumable`, an in-progress replacement
  // takes priority for the picker below over a stale "here is what built
  // the current game" note.
  const restored = !resumable && game.source === "file" ? (game.upload ?? null) : null;

  const [phase, setPhase] = useState<Phase>(resumable ? "resumable" : restored ? "done" : "idle");
  const [uploadId, setUploadId] = useState<string | null>(resumable?.id ?? restored?.id ?? null);
  const [filename, setFilename] = useState(resumable?.filename ?? restored?.filename ?? "");
  const [totalBytes, setTotalBytes] = useState(resumable?.declaredBytes ?? restored?.bytes ?? 0);
  const [sentBytes, setSentBytes] = useState(resumable?.receivedBytes ?? restored?.bytes ?? 0);
  const [rateBps, setRateBps] = useState(0);
  const [mismatch, setMismatch] = useState(false);
  const [errorCode, setErrorCode] = useState<string | null>(null);
  const [completedGame, setCompletedGame] = useState<Game | null>(restored ? game : null);
  // True only once this tab's own `runLoop` has actually finished an upload
  // — never for the "done" phase `restored` seeds above. It is what tells
  // the green "file received, build started" line (a fact about *this*
  // upload, just now) apart from `tu.sourceNote` (a fact about the file the
  // *current* game happens to have been built from, possibly long ago).
  const [liveCompletion, setLiveCompletion] = useState(false);
  // Mirrors whether `fileRef.current` is set, for render: a ref itself must
  // never be read while rendering (React warns, correctly — it is not a
  // value the render phase can depend on and still update as expected), so
  // "is there a file this tab can still retry with" needs its own state.
  const [hasFile, setHasFile] = useState(false);

  const fileRef = useRef<File | null>(null);
  const controllerRef = useRef<AbortController | null>(null);
  const rateOriginRef = useRef<{ time: number; bytes: number }>({ time: 0, bytes: 0 });

  const [windowFrom, setWindowFrom] = useState(1);
  const [windowLines, setWindowLines] = useState<string[]>([]);
  const [windowTotal, setWindowTotal] = useState(0);
  const [windowTruncated, setWindowTruncated] = useState(false);
  const [windowLoading, setWindowLoading] = useState(false);
  const [windowError, setWindowError] = useState<string | null>(null);
  const [gotoValue, setGotoValue] = useState("1");

  // Keeps this panel's own idea of the build current once its upload has
  // finished — GameEditor's own status tag above polls the same way, for
  // the same reason: nobody is holding a form open waiting on this one, a
  // build is minutes at the worst case, and "jump to the failing line"
  // below has nothing to jump to until a poll actually reports `failed`.
  useEffect(() => {
    if (phase !== "done" || !completedGame?.building) return;

    let live = true;
    const timer = setInterval(async () => {
      const fresh = await gameStatusAction(contestId);
      if (live && fresh) setCompletedGame(fresh);
    }, GAME_POLL_MS);

    return () => {
      live = false;
      clearInterval(timer);
    };
  }, [contestId, phase, completedGame?.building]);

  // Loads the viewer's first window the moment there is an upload id to read
  // it for — a live completion (`runLoop`, below) and a reload that restored
  // one from `game.upload` both reach `phase === "done"` this way, and
  // either one needs the same first page of lines before `tu.viewHeading`
  // means anything. `loadedForRef` is keyed on the upload id rather than
  // firing once per mount, so a second file uploaded later in the same tab
  // (a new id) still gets its own window loaded without this effect trying
  // on every re-render in between.
  const loadedForRef = useRef<string | null>(null);
  useEffect(() => {
    if (phase !== "done" || !uploadId || loadedForRef.current === uploadId) return;
    loadedForRef.current = uploadId;
    void fetchWindow(uploadId, 1);
    // fetchWindow is stable across renders (it closes over nothing but
    // setState calls and contestId), so it is deliberately left out of the
    // dependency array rather than redeclared with useCallback for a
    // function this effect is the only caller of.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [phase, uploadId]);

  function markProgress(newSent: number) {
    const elapsed = (Date.now() - rateOriginRef.current.time) / 1000;
    if (elapsed > 0.2) {
      setRateBps((newSent - rateOriginRef.current.bytes) / elapsed);
    }
    setSentBytes(newSent);
  }

  /**
   * `id` is always the caller's own, never read from `uploadId` state: this
   * is called from `runLoop` the instant an upload completes, inside the
   * same render pass that just called `setUploadId` — a state update that
   * has not landed in this closure's own `uploadId` yet, only in the next
   * render's. `loadWindow` below is the version a click handler uses, once
   * that render has long since happened and `uploadId` is trustworthy again.
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

  /** Sends every remaining chunk of `file`, starting at `startOffset`. */
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
        // Chunks are sent one at a time on purpose (the brief's own "слать
        // куски подряд"); a parallel Promise.all here would race PUTs
        // against the same offset, which the server would just refuse as
        // out of order.
        offset = await putChunk(contestId, id, offset, chunk, controller.signal);
        resyncs = 0;
        markProgress(offset);
      } catch (error) {
        if (isAbortError(error)) return;

        // The server knows the true offset better than this tab's own
        // count of what it sent — the rule the brief states for a resumed
        // upload applies just as much mid-stream, to a chunk this tab
        // thought had not landed yet but actually had.
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
    // The viewer's first window is loaded by the `loadedForRef` effect
    // above, keyed on `uploadId` (already `id` by the time that effect
    // reruns) — not fetched again here, which would only race it.
    // GameEditor's own status tag reads `initial`, a prop from the server
    // component above (page.tsx) — this is what brings it (and this
    // component's own `initialUpload`, now absent) current. Local state set
    // just above survives it: router.refresh() merges new server props into
    // the existing client tree without resetting useState (its own doc).
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

    // The reselected file only proves its own name and size match — the
    // server's own count of what it actually received is what a resend has
    // to start from, not the number this page loaded with, which may
    // already be stale.
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
    // Cleared immediately, not just after a successful read: without this, a
    // person who fixes a mismatched resume by picking the very same filename
    // a second time never fires `onChange` at all, because the input's own
    // value has not changed.
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
   * Back to the picker, so a different file can replace this one.
   *
   * Not cancel(): nothing is in flight, and the upload on the server is
   * complete rather than abandoned — it stays until a new one replaces it,
   * which is what makes this safe to offer beside a game that is already
   * built. The refusal when the contest has started is the server's to give
   * (game_not_editable), the same one the editor gets.
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
    return (errors as Record<string, string>)[code] ?? errors.fallback;
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
            <div
              className="h-full rounded-full bg-accent transition-[width] duration-(--t-input) ease-standard"
              style={{ width: `${percent}%` }}
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
                  {message(windowError)}
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
