"use client";

import { useActionState } from "react";

import { buttonVariants } from "@/components/ui/button";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { cn } from "@/lib/utils";

import { runQueryAction, type ConsoleState } from "./actions";

/**
 * The SQL console.
 *
 * Three states and never two at once: nothing asked yet, an answer, or a
 * refusal. A table left on screen under a red box is the shape that invites
 * reading yesterday's rows as the answer to today's question.
 *
 * The button is disabled while a query is in flight, and that is not polish:
 * a participant may have one query running at a time, so a second press earns
 * them a refusal they did nothing to deserve and cannot interpret.
 */
export function Console({ contestId, dict }: { contestId: string; dict: Dictionary }) {
  const t = dict.participant.console;
  const [state, run, running] = useActionState<ConsoleState, FormData>(runQueryAction, {
    kind: "idle",
  });

  return (
    <div className="flex flex-col gap-4">
      <form action={run} className="flex flex-col gap-3">
        <input type="hidden" name="contestId" value={contestId} />
        <label className="flex flex-col gap-2">
          <span className="text-label text-ink-2">{t.label}</span>
          <textarea
            name="sql"
            rows={6}
            spellCheck={false}
            placeholder={t.placeholder}
            className="min-h-40 w-full resize-y border border-edge bg-bg p-3 font-mono text-body text-ink outline-none focus-visible:border-accent"
          />
        </label>
        <div className="flex items-center gap-3">
          <button type="submit" disabled={running} className={cn(buttonVariants({ variant: "primary" }))}>
            {running ? t.running : t.run}
          </button>
          <span className="text-small text-ink-2">{t.hint}</span>
        </div>
      </form>

      {state.kind === "refused" ? <Refusal state={state} dict={dict} /> : null}
      {state.kind === "answer" ? <Answer result={state.result} dict={dict} /> : null}
    </div>
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

/** The rows, and whether they are all of them. */
function Answer({ result, dict }: { result: { columns: string[]; rows: (string | null)[][]; truncated: boolean; rows_affected: number }; dict: Dictionary }) {
  const t = dict.participant.console;

  if (result.columns.length === 0) {
    // A write answers with a count rather than with rows.
    return (
      <p role="status" className="border border-edge bg-surface p-3 text-body text-ink">
        {t.affected.replace("{count}", String(result.rows_affected))}
      </p>
    );
  }

  return (
    <div className="flex flex-col gap-2">
      {result.truncated ? (
        <p role="status" className="text-small text-ink-2">
          {t.truncated.replace("{count}", String(result.rows.length))}
        </p>
      ) : (
        <p className="text-small text-ink-2">
          {t.rowCount.replace("{count}", String(result.rows.length))}
        </p>
      )}

      <div className="overflow-x-auto border border-edge">
        <table className="w-full border-collapse text-body">
          <thead>
            <tr className="border-b border-edge bg-surface">
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

      {result.rows.length === 0 ? (
        <p className="text-small text-ink-2">{t.noRows}</p>
      ) : null}
    </div>
  );
}
