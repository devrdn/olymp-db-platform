"use client";

import { Button } from "@/components/ui/button";
import { toCsv } from "@/lib/format/csv";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import type { ConsoleState } from "./actions";

/**
 * What the last query produced, or why it did not — the "Result" tab of the
 * panel below the console.
 *
 * Reads `state` from the workspace rather than owning `useActionState`
 * itself: the form and its action live in `ConsoleEditor`, which stays
 * mounted and unchanged across a run so the textarea never remounts; this
 * component only renders whatever `ConsoleEditor` last reported. Being a
 * separate component — rather than folding this rendering back into the
 * editor — is what lets the workspace put it inside a *tab*, hidden without
 * unmounting when "Query log" is the one showing, while `ConsoleEditor`
 * itself stays outside every tab.
 */
export function ResultPanel({ state, dict }: { state: ConsoleState; dict: Dictionary }) {
  const t = dict.participant.console;

  if (state.kind === "idle") {
    return (
      <p className="p-4 text-body text-ink-2">{dict.participant.play.workspace.resultEmpty}</p>
    );
  }
  if (state.kind === "refused") {
    return (
      <div className="p-4">
        <Refusal state={state} dict={dict} />
      </div>
    );
  }

  const { result } = state;

  if (result.columns.length === 0) {
    // A write answers with a count rather than with rows.
    return (
      <p role="status" className="p-4 text-body text-ink">
        {t.affected.replace("{count}", String(result.rows_affected))}
      </p>
    );
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2 p-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        {result.truncated ? (
          <p role="status" className="text-small text-ink-2">
            {t.truncated.replace("{count}", String(result.rows.length))}
          </p>
        ) : (
          <p className="text-small text-ink-2">{t.rowCount.replace("{count}", String(result.rows.length))}</p>
        )}
        <DownloadButton columns={result.columns} rows={result.rows} label={dict.participant.play.workspace.download} />
      </div>

      <div className="min-h-0 flex-1 overflow-auto border border-edge">
        <table className="w-full border-collapse text-body">
          <thead className="sticky top-0 bg-surface">
            <tr className="border-b border-edge">
              {result.columns.map((column, i) => (
                <th key={`${column}-${i}`} className="p-2 text-left font-medium text-ink">
                  {column}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {result.rows.map((row, r) => (
              <tr key={r} className="border-b border-edge last:border-b-0">
                {row.map((cell, c) => (
                  <td key={c} className="p-2 align-top font-mono text-ink">
                    {/* NULL and the empty string are different things in SQL,
                        and a participant debugging a left join is looking at
                        exactly that column. */}
                    {cell === null ? <span className="text-ink-2 italic">{t.null}</span> : cell}
                  </td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {result.rows.length === 0 ? <p className="text-small text-ink-2">{t.noRows}</p> : null}
    </div>
  );
}

/**
 * Builds the CSV client-side, from exactly the columns and rows already on
 * screen, and hands it to the browser — no second request, no server
 * involvement. That is also the boundary the plan draws (Task 3): the
 * runner's own budget already truncated `result.rows` before this component
 * ever saw it, so there is no way for this button to carry more than the
 * table above it already showed.
 *
 * `URL.createObjectURL` plus a click on a detached `<a download>` is the
 * ordinary way to save a browser-generated file without a round trip; the
 * object URL is revoked right after, so nothing here holds memory past the
 * click that used it.
 */
function DownloadButton({
  columns,
  rows,
  label,
}: {
  columns: readonly string[];
  rows: readonly (string | null)[][];
  label: string;
}) {
  return (
    <Button
      type="button"
      variant="quiet"
      size="sm"
      onClick={() => {
        const csv = toCsv(columns, rows);
        const url = URL.createObjectURL(new Blob([csv], { type: "text/csv;charset=utf-8" }));
        const link = document.createElement("a");
        link.href = url;
        link.download = `query-result-${Date.now()}.csv`;
        link.click();
        URL.revokeObjectURL(url);
      }}
    >
      {label}
    </Button>
  );
}

/**
 * Why a query did not run.
 *
 * The sentence comes from the dictionary by code and the subject is appended,
 * because "that function is not available" without naming it is the same
 * unactionable answer whichever language it is in.
 */
function Refusal({ state, dict }: { state: Extract<ConsoleState, { kind: "refused" }>; dict: Dictionary }) {
  const errors = dict.errors as Record<string, string>;
  const message = errors[state.code] ?? errors.fallback;

  // Waiting is a different situation from being wrong, and the participant
  // should be able to tell without reading carefully: one of these means try
  // again in a moment, the other means change the query.
  const passing = ["query_busy", "query_already_running", "query_too_often", "no_game_yet"].includes(
    state.code,
  );

  return (
    <div
      role="status"
      className={cn(
        "border p-3 text-body",
        passing ? "border-edge bg-surface text-ink" : "border-danger/40 bg-danger/5 text-ink",
      )}
    >
      {message}
      {state.subject ? <span className="ml-1 font-mono text-ink-2">{state.subject}</span> : null}
      {/* Shown only where something went wrong on our side, because that is
          the only case where anybody will be asked for it — and a reference
          number printed under an ordinary refusal reads as though the refusal
          were a fault. */}
      {state.requestId && !passing ? (
        <p className="mt-2 font-mono text-small text-ink-3">
          {dict.participant.console.reference.replace("{id}", state.requestId)}
        </p>
      ) : null}
    </div>
  );
}
