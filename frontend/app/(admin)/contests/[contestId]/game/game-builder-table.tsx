"use client";

import { useEffect, useRef, useState } from "react";

import { buttonVariants } from "@/components/ui/button";
import { ApiError, request } from "@/lib/api/client";
import type { BuilderLimits, TableDefinition } from "@/lib/api/game";
import { readableBytes, readableDuration } from "@/lib/format/bytes";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import {
  abortTableUploadAction,
  appendTableRowAction,
  beginTableUploadAction,
  completeTableUploadAction,
  deleteTableRowAction,
  gameTableDataWindowAction,
} from "./actions";

type Phase = "idle" | "uploading" | "completing" | "error";

/** `isAbortError` from `game-upload.tsx` — the identical check, for the
 * identical reason (that file's own doc: `DOMException` is not reliably an
 * `Error` across environments, and a cancelled upload must never read as an
 * unreachable server). Copied rather than imported: neither file exports
 * anything today, and importing across two sibling "use client" leaves for
 * a four-line predicate would be the tighter coupling. */
function isAbortError(error: unknown): boolean {
  return (
    typeof error === "object" &&
    error !== null &&
    (error as { name?: unknown }).name === "AbortError"
  );
}

function failureCode(error: unknown): string {
  return error instanceof ApiError ? error.code : "unreachable";
}

/**
 * One chunk of a table's own CSV upload, sent straight to the API rather
 * than through a Server Action — `putChunk` in `game-upload.tsx`, for a
 * table's chunk endpoint instead of a dump's. That file's own doc gives the
 * reasoning in full: a Server Action's body is capped far below
 * `limits.chunkBytes`, which is this *installation's own* configured
 * ceiling (`provisioning.Games.TableDataLimits`), never a constant this file
 * could match, and a Server Action would buffer the whole chunk as
 * `FormData` before this code ever ran where `rawBody: Blob` lets the
 * browser stream the slice off disk instead.
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
 * One table's own data: its rows, a page at a time, added by hand or loaded
 * from a CSV file in chunks — the table builder's own counterpart of
 * `GameUpload`, scoped to a single table rather than the whole game.
 *
 * Kept mounted for every table the definition currently names (its caller,
 * `GameBuilder`, renders one of these per table inside a `Tabs.Content` that
 * only *hides* the ones not selected), so switching tabs never loses an
 * upload already under way. `active` is what gates the very first fetch —
 * this component does nothing on mount until the organiser has actually
 * looked at it, the same "read only what the screen needs" reasoning
 * `page.tsx`'s own `tableRowCount` gives for a page load that would
 * otherwise fan out to every table at once.
 *
 * There is no `.../data/current` route for a table's own upload the way a
 * dump has one at `.../uploads/current` (`game_handler.go`'s own routes) —
 * this task's brief lists the table builder's contract exhaustively and it
 * is simply not among them. Two things follow from that gap, both worth
 * naming rather than working around silently: a page reload mid-upload
 * cannot resume it (the browser's own memory of the offset is the only copy
 * that exists), and a chunk refused as out of order cannot resynchronise
 * against the server's own count the way `game-upload.tsx`'s own retry loop
 * does for a dump — there is nothing to ask. `sentBytes` is still never
 * advanced except from a chunk response's own `received_bytes`, so an
 * ordinary retry (the same bytes, from the same offset) is the idempotent
 * no-op `gamefile.Store.Append` already promises, and is what "Retry" below
 * sends; only two callers racing the same table's upload has any other
 * outcome, and that is answered honestly (the refusal's own translated
 * sentence) rather than guessed at.
 */
export function GameBuilderTable({
  contestId,
  table,
  active,
  limits,
  editable,
  rowCount,
  onRowCountChange,
  dict,
}: {
  contestId: string;
  /** The table's current, saved structure — never edited from here; only
   * `GameBuilder`'s own definition editor changes it. */
  table: TableDefinition;
  /** Whether this table's tab is the one currently shown. */
  active: boolean;
  limits: BuilderLimits;
  editable: boolean;
  /** This table's current row count, lifted into `GameBuilder`'s own state
   * so every tab agrees on it without a second read. */
  rowCount: number;
  onRowCountChange: (rowCount: number) => void;
  dict: Dictionary;
}) {
  const tb = dict.workspace.game.builder;
  const td = tb.data;
  const errors = dict.errors;

  const [phase, setPhase] = useState<Phase>("idle");
  const [uploadId, setUploadId] = useState<string | null>(null);
  const [filename, setFilename] = useState("");
  const [totalBytes, setTotalBytes] = useState(0);
  const [sentBytes, setSentBytes] = useState(0);
  const [rateBps, setRateBps] = useState(0);
  const [errorCode, setErrorCode] = useState<string | null>(null);
  // The server's own words, when the refusal is one of the table builder's
  // own row/column-specific ones — `GameState.detail`'s own doc in
  // `actions.ts` explains why this is the one class of error whose English
  // text is worth showing beside the dictionary's own translated sentence.
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
    // `w.totalRows` is `TableRowWindow.TotalRows` — `data.Lines` on the
    // server (`tabledata.go`'s own `TableDataWindow`), never adjusted for a
    // tombstoned row the way `TableData.ActiveRows()` is. That is the more
    // conservative of the two counts, and deliberately the one this passes
    // upward: `GameBuilder`'s own lock on a table's structure exists because
    // its CSV file still names the *old* columns, and that stays true of a
    // fully-deleted file exactly as much as a full one — nothing about a
    // tombstone rewrites the header. `appendTableRowAction` and
    // `completeTableUploadAction` report the exact, deletion-adjusted count
    // (`TableData.ActiveRows()`) after a write that actually changes it,
    // which is the more honest number for the row list's own heading.
    onRowCountChange(w.totalRows);
  }

  // Loads the first page the moment this table's tab is actually looked at,
  // and never again on its own afterward — `loadedRef` is this instance's
  // own flag, not keyed on anything that changes while the component stays
  // mounted (`GameBuilderTable`'s own doc explains why it stays mounted
  // across a tab switch).
  const loadedRef = useRef(false);
  useEffect(() => {
    if (!active || loadedRef.current) return;
    loadedRef.current = true;
    void fetchWindow(1);
    // fetchWindow is stable enough for this effect's one-time call; see the
    // identical choice in game-upload.tsx's own loadedForRef effect.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active]);

  function markProgress(newSent: number) {
    const elapsed = (Date.now() - rateOriginRef.current.time) / 1000;
    if (elapsed > 0.2) {
      setRateBps((newSent - rateOriginRef.current.bytes) / elapsed);
    }
    setSentBytes(newSent);
  }

  /** Sends every remaining chunk of `file`, starting at `startOffset` —
   * `runLoop` in `game-upload.tsx`, minus the out-of-order resync this
   * component's own doc explains there is nothing left to ask for. */
  async function runLoop(file: File, id: string, startOffset: number, chunkBytes: number) {
    const controller = new AbortController();
    controllerRef.current = controller;
    rateOriginRef.current = { time: Date.now(), bytes: startOffset };

    let offset = startOffset;

    while (offset < file.size) {
      const end = Math.min(offset + chunkBytes, file.size);
      const chunk = file.slice(offset, end);

      try {
        // One chunk at a time, on purpose — the identical reasoning
        // game-upload.tsx's own runLoop gives: a parallel PUT would race the
        // same offset and be refused as out of order regardless.
        offset = await putTableChunk(contestId, table.name, id, offset, chunk, controller.signal);
        markProgress(offset);
      } catch (error) {
        if (isAbortError(error)) return;
        setPhase("error");
        setErrorCode(failureCode(error));
        // The chunk PUT is sent straight to the API (`putTableChunk`'s own
        // doc), not through a Server Action, so the full `ApiError` — header
        // mismatch and everything else `checkTableHeaderOnFirstChunk` can
        // catch this early — is still right here to read `.message` off,
        // never routed through `GameState.detail` at all.
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
    if (result.value) onRowCountChange(result.value.activeRows);
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

  function handleFileChange(event: React.ChangeEvent<HTMLInputElement>) {
    const picked = event.target.files?.[0];
    event.target.value = "";
    if (!picked) return;
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
    return (errors as Record<string, string>)[code] ?? errors.fallback;
  }

  function updateRowValue(index: number, value: string) {
    setRowValues((prev) => prev.map((v, i) => (i === index ? value : v)));
  }

  async function submitRow(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setAddErrorCode(null);
    setAddErrorDetail(null);

    // Checked before a request is made: an empty value in a column the
    // definition marked NOT NULL is exactly what the server would refuse
    // (`game_table_value_invalid`'s own translated sentence already says so
    // — "The message says which row and column"), and this is the one such
    // mistake cheap enough to name before the click rather than after it.
    // There is no server detail to show for it: nothing was sent yet.
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
    if (result.value) onRowCountChange(result.value.activeRows);
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
    onRowCountChange(Math.max(0, rowCount - 1));
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

/** A value input shaped for one column's own type — the closed set
 * `provisioning.ColumnType.valid` accepts, read here off `col.type` as a
 * plain string the same way `columnDefinitionSchema` carries it (never a
 * second, hand-typed enum on this side — the reasoning that schema's own
 * doc gives). An unrecognised type (a future column type this build does
 * not know about yet) falls back to a plain text field rather than refusing
 * to render at all: the server is still what validates the value either
 * way. */
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
  // "numeric" stays a plain text field rather than type="number": PostgreSQL's
  // own numeric_in accepts digit-grouping underscores and the special values
  // "NaN"/"Infinity", none of which a browser's number input would let
  // through — validateScalar (tablecsv.go) is the actual check either way.
  return <input type="text" value={value} onChange={(event) => onChange(event.target.value)} className={className} />;
}
