"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";

import { buttonVariants } from "@/components/ui/button";
import { ApiError, failureCode, isAbortError, request } from "@/lib/api/client";
import type { BuilderLimits, TableData, TableDefinition } from "@/lib/api/game";
import { readableBytes, readableDuration } from "@/lib/format/bytes";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import {
  abortTableUploadAction,
  appendTableRowAction,
  beginTableUploadAction,
  completeTableUploadAction,
  currentTableUploadAction,
  deleteTableRowAction,
  gameTableDataWindowAction,
} from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

type Phase = "idle" | "resumable" | "uploading" | "completing" | "error";

/**
 * Consecutive out-of-order refusals resynced automatically before asking for
 * Retry (as in `game-upload.tsx`).
 */
const MAX_AUTO_RESYNCS = 3;

/**
 * Sends one chunk straight to the API: a Server Action's body limit is far
 * below the configured chunk size, and `rawBody: Blob` streams the slice from
 * disk instead of buffering it as `FormData`.
 */
async function putTableChunk(
  contestId: string,
  table: string,
  id: string,
  offset: number,
  chunk: Blob,
  signal: AbortSignal,
): Promise<number> {
  const payload = await request(
    `/contests/${contestId}/game/tables/${encodeURIComponent(table)}/data/${id}/chunk?offset=${offset}`,
    { method: "PUT", rawBody: chunk, signal },
  );

  const bytes = (payload as { received_bytes?: unknown } | null)?.received_bytes;
  if (typeof bytes !== "number") {
    throw new Error(`malformed chunk response for table upload ${id}`);
  }
  return bytes;
}

/**
 * One table's data: rows a page at a time, added by hand or loaded from a
 * chunked CSV.
 *
 * Stays mounted for every table (tabs only hide), so a running upload survives
 * a tab switch; `active` gates the first fetch so a page load does not fan out
 * to every table. A reload resumes from `initialTableData`, and an out-of-order
 * chunk resyncs against the server's count. `sentBytes` only advances from a
 * chunk response's `received_bytes`, so Retry resends idempotently.
 */
export function GameBuilderTable({
  contestId,
  table,
  active,
  limits,
  editable,
  onRowCountChange,
  activeRowCount,
  onActiveRowCountChange,
  initialTableData,
  dict,
}: {
  contestId: string;
  /** The saved structure; only `GameBuilder` edits it. */
  table: TableDefinition;
  active: boolean;
  limits: BuilderLimits;
  editable: boolean;
  /**
   * Reports `TableData.Lines`. Not the displayed number: a tombstone never
   * rewrites the file, so `Lines` is what the structure lock checks and must
   * not drop on a delete (see `activeRowCount`).
   */
  onRowCountChange: (rowCount: number) => void;
  /**
   * Active rows (`Lines` minus tombstones), the number shown. Kept apart from
   * `onRowCountChange` because a delete lowers only this one.
   */
  activeRowCount: number;
  onActiveRowCountChange: (activeRowCount: number) => void;
  /** The upload still receiving at page load, or null. Read once on mount. */
  initialTableData: TableData | null;
  dict: Dictionary;
}) {
  const tb = dict.workspace.game.builder;
  const td = tb.data;
  const errors = dict.errors;
  const router = useRouter();

  /**
   * Refreshes server props after a write that marks the game out of date.
   * Revalidating inside the action is not enough: a plain action call, unlike a
   * form submit, does not refresh the tree. Safe mid-upload, since a refresh
   * merges props without resetting state.
   */
  function refreshGameState() {
    router.refresh();
  }

  // Only a `'receiving'` upload is resumable; the server returns no other, but
  // this does not trust it.
  const resumable = initialTableData && initialTableData.status === "receiving" ? initialTableData : null;

  const [phase, setPhase] = useState<Phase>(resumable ? "resumable" : "idle");
  const [uploadId, setUploadId] = useState<string | null>(resumable?.id ?? null);
  const [filename, setFilename] = useState("");
  const [totalBytes, setTotalBytes] = useState(resumable?.declaredBytes ?? 0);
  const [sentBytes, setSentBytes] = useState(resumable?.receivedBytes ?? 0);
  const [rateBps, setRateBps] = useState(0);
  // Set while resuming with a file whose size does not match; a table upload
  // has no filename to compare.
  const [mismatch, setMismatch] = useState(false);
  const [errorCode, setErrorCode] = useState<string | null>(null);
  // The server's row/column detail for a table-builder refusal (see
  // `GameState.detail`).
  const [errorDetail, setErrorDetail] = useState<string | null>(null);
  const [hasFile, setHasFile] = useState(false);

  const fileRef = useRef<File | null>(null);
  const controllerRef = useRef<AbortController | null>(null);
  const rateOriginRef = useRef<{ time: number; bytes: number }>({ time: 0, bytes: 0 });

  const [windowFrom, setWindowFrom] = useState(1);
  const [windowRows, setWindowRows] = useState<{ row: number; fields: string[] }[]>([]);
  const [windowTotal, setWindowTotal] = useState(0);
  const [windowTruncated, setWindowTruncated] = useState(false);
  const [windowLoading, setWindowLoading] = useState(false);
  const [windowError, setWindowError] = useState<string | null>(null);
  const [gotoValue, setGotoValue] = useState("1");

  const [rowValues, setRowValues] = useState<string[]>(() => table.columns.map(() => ""));
  const [adding, setAdding] = useState(false);
  const [addErrorCode, setAddErrorCode] = useState<string | null>(null);
  const [addErrorDetail, setAddErrorDetail] = useState<string | null>(null);
  const [deletingRow, setDeletingRow] = useState<number | null>(null);

  async function fetchWindow(from: number) {
    setWindowLoading(true);
    setWindowError(null);
    const result = await gameTableDataWindowAction(contestId, table.name, Math.max(1, from));
    setWindowLoading(false);
    if (result.code) {
      setWindowError(result.code);
      return;
    }
    const w = result.value;
    if (!w) return;
    setWindowFrom(w.fromRow);
    setWindowRows(w.rows);
    setWindowTotal(w.totalRows);
    setWindowTruncated(w.truncated);
    setGotoValue(String(w.fromRow));
    // `totalRows` is `Lines`, not adjusted for tombstones: the structure lock
    // must hold while the file still names the old columns.
    onRowCountChange(w.totalRows);
  }

  // Loads the first page once, when the tab is first shown.
  const loadedRef = useRef(false);
  useEffect(() => {
    if (!active || loadedRef.current) return;
    loadedRef.current = true;
    void fetchWindow(1);
    // One-time call.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active]);

  function markProgress(newSent: number) {
    const elapsed = (Date.now() - rateOriginRef.current.time) / 1000;
    if (elapsed > 0.2) {
      setRateBps((newSent - rateOriginRef.current.bytes) / elapsed);
    }
    setSentBytes(newSent);
  }

  /** Sends the remaining chunks from `startOffset`, resyncing on out-of-order refusals. */
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
        offset = await putTableChunk(contestId, table.name, id, offset, chunk, controller.signal);
        resyncs = 0;
        markProgress(offset);
      } catch (error) {
        if (isAbortError(error)) return;

        // The server knows the true offset better than this tab.
        if (
          error instanceof ApiError &&
          error.code === "game_table_data_chunk_out_of_order" &&
          resyncs < MAX_AUTO_RESYNCS
        ) {
          resyncs += 1;
          const fresh = await currentTableUploadAction(contestId, table.name);
          if (fresh && fresh.id === id && fresh.status === "receiving") {
            offset = fresh.receivedBytes;
            markProgress(offset);
            continue;
          }
        }

        setPhase("error");
        setErrorCode(failureCode(error));
        // The PUT goes straight to the API, so the full `ApiError` (e.g. a
        // header mismatch) is available here.
        setErrorDetail(error instanceof ApiError ? error.message : null);
        return;
      }
    }

    setPhase("completing");
    const result = await completeTableUploadAction(contestId, table.name, id);
    if (result.code) {
      setPhase("error");
      setErrorCode(result.code);
      setErrorDetail(result.detail ?? null);
      return;
    }

    setPhase("idle");
    setHasFile(false);
    fileRef.current = null;
    setUploadId(null);
    if (result.value) {
      onRowCountChange(result.value.lines);
      onActiveRowCountChange(result.value.activeRows);
    }
    refreshGameState();
    await fetchWindow(1);
  }

  async function beginFresh(file: File) {
    setFilename(file.name);
    setTotalBytes(file.size);
    setSentBytes(0);
    setErrorCode(null);
    setErrorDetail(null);
    setPhase("uploading");

    const result = await beginTableUploadAction(contestId, table.name, file.size);
    const begun = result.value;
    if (result.code || !begun) {
      setPhase("error");
      setErrorCode(result.code ?? "unreachable");
      return;
    }

    fileRef.current = file;
    setHasFile(true);
    setUploadId(begun.id);
    await runLoop(file, begun.id, 0, begun.builderLimits.chunkBytes);
  }

  /** Resumes after a reload; only the reselected file's size must match. */
  async function beginResume(file: File) {
    if (!resumable) return;
    setMismatch(false);
    setErrorCode(null);
    setErrorDetail(null);
    setPhase("uploading");

    // Resend from the server's current count; the one loaded with the page may
    // be stale.
    const fresh = await currentTableUploadAction(contestId, table.name);
    const offset =
      fresh && fresh.id === resumable.id && fresh.status === "receiving"
        ? fresh.receivedBytes
        : resumable.receivedBytes;

    fileRef.current = file;
    setHasFile(true);
    setUploadId(resumable.id);
    setFilename(file.name);
    setTotalBytes(resumable.declaredBytes);
    setSentBytes(offset);
    await runLoop(file, resumable.id, offset, (fresh ?? resumable).builderLimits.chunkBytes);
  }

  function handleFileChange(event: React.ChangeEvent<HTMLInputElement>) {
    const picked = event.target.files?.[0];
    // Cleared at once, so picking the same file again still fires `onChange`.
    event.target.value = "";
    if (!picked) return;

    if (phase === "resumable" && resumable) {
      if (picked.size !== resumable.declaredBytes) {
        setMismatch(true);
        return;
      }
      void beginResume(picked);
      return;
    }

    void beginFresh(picked);
  }

  async function cancel() {
    if (!window.confirm(td.cancelConfirm)) return;
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
    setErrorDetail(null);
    if (id) await abortTableUploadAction(contestId, table.name, id);
  }

  async function retry() {
    const file = fileRef.current;
    const id = uploadId;
    if (!file || !id) return;
    setErrorCode(null);
    setErrorDetail(null);
    setPhase("uploading");
    await runLoop(file, id, sentBytes, limits.chunkBytes);
  }

  function message(code: string): string {
    return messageForCode(code, errors);
  }

  function updateRowValue(index: number, value: string) {
    setRowValues((prev) => prev.map((v, i) => (i === index ? value : v)));
  }

  async function submitRow(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setAddErrorCode(null);
    setAddErrorDetail(null);

    // An empty value in a NOT NULL column is refused before any request.
    const missing = table.columns.findIndex((col, i) => !col.nullable && rowValues[i].trim() === "");
    if (missing >= 0) {
      setAddErrorCode("game_table_value_invalid");
      return;
    }

    setAdding(true);
    const result = await appendTableRowAction(contestId, table.name, rowValues);
    setAdding(false);
    if (result.code) {
      setAddErrorCode(result.code);
      setAddErrorDetail(result.detail ?? null);
      return;
    }
    setRowValues(table.columns.map(() => ""));
    if (result.value) {
      onRowCountChange(result.value.lines);
      onActiveRowCountChange(result.value.activeRows);
    }
    refreshGameState();
    await fetchWindow(windowFrom);
  }

  async function removeRow(row: number) {
    if (!window.confirm(td.deleteRowConfirm.replace("{row}", String(row)))) return;
    setDeletingRow(row);
    const result = await deleteTableRowAction(contestId, table.name, row);
    setDeletingRow(null);
    if (result.code) {
      setWindowError(result.code);
      return;
    }
    // A tombstone never rewrites `Lines`, so only the active count moves; the
    // structure lock must stay.
    onActiveRowCountChange(Math.max(0, activeRowCount - 1));
    refreshGameState();
    await fetchWindow(windowFrom);
  }

  if (!limits.enabled) {
    return <p className="max-w-body text-small text-ink-2">{td.dataDisabled}</p>;
  }

  const percent = totalBytes > 0 ? Math.min(100, Math.floor((sentBytes / totalBytes) * 100)) : 0;
  const remaining = totalBytes - sentBytes;
  const etaSeconds = rateBps > 0 ? remaining / rateBps : null;

  return (
    <div className="flex flex-col gap-5">
      <div className="flex flex-col gap-2.5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h5 className="text-control text-ink">{td.rowsHeading}</h5>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              const row = Number(gotoValue);
              if (Number.isFinite(row) && row >= 1) void fetchWindow(Math.floor(row));
            }}
            className="flex flex-wrap items-center gap-2"
          >
            <label className="text-label text-ink-3" htmlFor={`goto-${table.name}`}>
              {td.gotoLabel}
            </label>
            <input
              id={`goto-${table.name}`}
              type="number"
              min={1}
              value={gotoValue}
              onChange={(event) => setGotoValue(event.target.value)}
              className="h-7 w-24 border border-line-2 bg-transparent px-2 text-control-sm text-ink outline-none"
            />
            <button type="submit" className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}>
              {td.gotoButton}
            </button>
            <button
              type="button"
              disabled={windowFrom <= 1}
              onClick={() => void fetchWindow(Math.max(1, windowFrom - (windowRows.length || 1)))}
              className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
            >
              {td.prev}
            </button>
            <button
              type="button"
              disabled={windowFrom + windowRows.length - 1 >= windowTotal}
              onClick={() => void fetchWindow(windowFrom + windowRows.length)}
              className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
            >
              {td.next}
            </button>
            {windowTotal > 0 ? (
              <span className="text-label text-ink-3">{td.totalRows.replace("{n}", String(windowTotal))}</span>
            ) : null}
          </form>
        </div>

        {windowError ? (
          <p role="alert" className="text-small text-bad">
            {(errors as Record<string, string>)[windowError] ?? td.windowError}
          </p>
        ) : windowLoading ? (
          <p className="text-small text-ink-3">{td.loading}</p>
        ) : windowRows.length === 0 ? (
          <p className="text-small text-ink-2">{td.emptyRows}</p>
        ) : (
          <div className="overflow-x-auto border border-line-2 bg-sunk">
            <div className="min-w-max">
              <div className="flex gap-3 border-b border-line-2 px-3 py-1 font-mono text-label text-ink-3 uppercase">
                <span className="w-12 shrink-0 text-right">{td.gotoLabel}</span>
                {table.columns.map((col) => (
                  <span key={col.name} className="w-40 shrink-0">
                    {col.name}
                  </span>
                ))}
                <span className="w-16 shrink-0" />
              </div>
              {windowRows.map((row) => (
                <div key={row.row} className="flex gap-3 px-3 py-0.5 font-mono text-data odd:bg-panel">
                  <span className="w-12 shrink-0 select-none text-right text-ink-3">{row.row}</span>
                  {row.fields.map((field, i) => (
                    <span key={i} className="w-40 shrink-0 truncate whitespace-pre text-ink">
                      {field}
                    </span>
                  ))}
                  <span className="w-16 shrink-0">
                    {editable ? (
                      <button
                        type="button"
                        disabled={deletingRow === row.row}
                        onClick={() => void removeRow(row.row)}
                        className={cn(buttonVariants({ variant: "danger", size: "sm" }))}
                      >
                        {td.deleteRow}
                      </button>
                    ) : null}
                  </span>
                </div>
              ))}
            </div>
          </div>
        )}
        {windowTruncated ? <p className="text-small text-ink-2">{td.windowTruncated}</p> : null}
      </div>

      {editable ? (
        <div className="flex flex-col gap-2.5">
          <h5 className="text-control text-ink">{td.addRowHeading}</h5>
          <form onSubmit={submitRow} className="flex flex-col gap-3">
            <div className="flex flex-wrap items-end gap-3">
              {table.columns.map((col, i) => (
                <label key={col.name} className="flex flex-col gap-1">
                  <span className="text-label text-ink-3">
                    {col.name}
                    {col.nullable ? ` (${td.nullPlaceholder})` : ""}
                  </span>
                  <ColumnValueInput
                    type={col.type}
                    value={rowValues[i] ?? ""}
                    onChange={(value) => updateRowValue(i, value)}
                    nullable={col.nullable}
                  />
                </label>
              ))}
              <button
                type="submit"
                disabled={adding || table.columns.length === 0}
                className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
              >
                {adding ? td.adding : td.addRowButton}
              </button>
            </div>
            {addErrorCode ? (
              <div className="flex flex-col gap-1">
                <p role="alert" className="text-small text-bad">
                  {message(addErrorCode)}
                </p>
                {addErrorDetail ? (
                  <>
                    <p className="text-label text-ink-3">{tb.detailLabel}</p>
                    <pre className="max-w-body overflow-x-auto font-mono text-data text-bad">{addErrorDetail}</pre>
                  </>
                ) : null}
              </div>
            ) : null}
          </form>

          <div className="flex flex-col gap-2 border-t border-line pt-4">
            <h5 className="text-control text-ink">{td.csvHeading}</h5>
            <p className="max-w-body text-small text-ink-2">
              {td.csvLede.replace("{columns}", table.columns.map((c) => c.name).join(", "))}
            </p>

            {phase === "idle" ? (
              <div className="flex flex-wrap items-center gap-3">
                <label className={cn(buttonVariants({ variant: "secondary" }), "cursor-pointer")}>
                  {td.pick}
                  <input type="file" className="sr-only" onChange={handleFileChange} />
                </label>
                <span className="text-label text-ink-3">
                  {td.limitHint.replace("{max}", readableBytes(limits.maxFileBytes))}
                </span>
              </div>
            ) : null}

            {phase === "resumable" && resumable ? (
              <div className="flex flex-col gap-2 border-l-2 border-warn pl-4">
                <p className="text-control text-ink">{td.resumeHeading}</p>
                <p className="max-w-body text-small text-ink-2">
                  {td.resumeBody
                    .replace("{received}", readableBytes(resumable.receivedBytes))
                    .replace("{total}", readableBytes(resumable.declaredBytes))}
                </p>
                {mismatch ? (
                  <p role="alert" className="max-w-body text-small text-bad">
                    {td.resumeMismatch.replace("{total}", readableBytes(resumable.declaredBytes))}
                  </p>
                ) : null}
                <div className="flex flex-wrap items-center gap-3">
                  <label className={cn(buttonVariants({ variant: "secondary" }), "cursor-pointer")}>
                    {td.resumePick}
                    <input type="file" className="sr-only" onChange={handleFileChange} />
                  </label>
                  <button
                    type="button"
                    onClick={() => void cancel()}
                    className={cn(buttonVariants({ variant: "quiet" }))}
                  >
                    {td.resumeCancel}
                  </button>
                </div>
              </div>
            ) : null}

            {phase === "uploading" || phase === "completing" ? (
              <div className="flex flex-col gap-2.5">
                <div className="flex items-center justify-between gap-3">
                  <span className="truncate font-mono text-data text-ink">{filename}</span>
                  <span className="text-label text-warn">{phase === "completing" ? td.completing : td.uploading}</span>
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
                    {td.progress
                      .replace("{sent}", readableBytes(sentBytes))
                      .replace("{total}", readableBytes(totalBytes))
                      .replace("{percent}", String(percent))}
                  </span>
                  {rateBps > 0 ? <span>{td.rate.replace("{rate}", readableBytes(rateBps))}</span> : null}
                  {etaSeconds !== null ? (
                    <span>{td.eta.replace("{time}", readableDuration(etaSeconds))}</span>
                  ) : null}
                </div>
                {phase === "uploading" ? (
                  <div>
                    <button
                      type="button"
                      onClick={() => void cancel()}
                      className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
                    >
                      {td.cancel}
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
                {errorDetail ? (
                  <div className="flex flex-col gap-1">
                    <p className="text-label text-ink-3">{tb.detailLabel}</p>
                    <pre className="max-w-body overflow-x-auto font-mono text-data text-bad">{errorDetail}</pre>
                  </div>
                ) : null}
                <div className="flex flex-wrap items-center gap-3">
                  {hasFile && uploadId ? (
                    <button
                      type="button"
                      onClick={() => void retry()}
                      className={cn(buttonVariants({ variant: "secondary", size: "sm" }))}
                    >
                      {td.retry}
                    </button>
                  ) : null}
                  <button
                    type="button"
                    onClick={() => void cancel()}
                    className={cn(buttonVariants({ variant: "quiet", size: "sm" }))}
                  >
                    {td.cancel}
                  </button>
                </div>
              </div>
            ) : null}
          </div>
        </div>
      ) : null}
    </div>
  );
}

/**
 * An input shaped for the column type. An unknown type falls back to a text
 * field; the server validates either way.
 */
function ColumnValueInput({
  type,
  value,
  onChange,
  nullable,
}: {
  type: string;
  value: string;
  onChange: (value: string) => void;
  nullable: boolean;
}) {
  const className = cn(
    "h-(--control-h) w-36 rounded-none border border-edge bg-transparent px-3",
    "text-control text-ink placeholder:text-ink-3",
    "transition-colors duration-(--t-input) ease-standard",
    "hover:border-ink-2 focus-visible:border-ink",
  );

  if (type === "boolean") {
    return (
      <select value={value} onChange={(event) => onChange(event.target.value)} className={className}>
        {nullable ? <option value="" /> : null}
        <option value="true">true</option>
        <option value="false">false</option>
      </select>
    );
  }
  if (type === "date") {
    return <input type="date" value={value} onChange={(event) => onChange(event.target.value)} className={className} />;
  }
  if (type === "timestamp") {
    return (
      <input
        type="datetime-local"
        step={1}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className={className}
      />
    );
  }
  if (type === "integer") {
    return (
      <input
        type="number"
        step={1}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        className={className}
      />
    );
  }
  // Text, not type="number": PostgreSQL numeric accepts underscores, "NaN" and
  // "Infinity", which a number input rejects.
  return <input type="text" value={value} onChange={(event) => onChange(event.target.value)} className={className} />;
}
