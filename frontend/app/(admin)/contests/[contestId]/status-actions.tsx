"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import type { ContestStatus } from "@/lib/api/contests";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { setStatusAction, type StatusState } from "./actions";

/**
 * The transitions a contest can be moved through, as buttons.
 *
 * Publishing is the only one that can be blocked by unfinished work, and it is
 * blocked *with the reason beside it* rather than greyed out in silence — the
 * gate's summary is rendered above this row. Everything else is offered
 * whenever the mirrored transition table allows it.
 *
 * Publishing gets the one dark button; every other move is outlined. Archiving
 * is the exception that reads as a discard, so it takes the danger variant. A
 * contest is never deleted from here: deletion is a different act with a
 * different endpoint, and putting it in a row of state changes is how it gets
 * pressed by somebody meaning to archive.
 */
export function StatusActions({
  contestId,
  next,
  blocked,
  dict,
}: {
  contestId: string;
  next: readonly ContestStatus[];
  /** Publishing is impossible until the gate passes; the reason is shown above. */
  blocked?: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace;
  const [state, formAction, pending] = useActionState<StatusState, FormData>(setStatusAction, {});

  const failure = state.code
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
    : null;

  if (next.length === 0) {
    return <p className="text-small text-ink-3">{t.status.terminal}</p>;
  }

  return (
    <form action={formAction} className="flex flex-col gap-3">
      <input type="hidden" name="contestId" value={contestId} />

      <div className="flex flex-wrap items-center gap-2.5">
        {next.map((status) => {
          const stopped = status === "published" && blocked;

          return (
            <Button
              key={status}
              type="submit"
              name="status"
              value={status}
              disabled={pending || stopped}
              variant={
                status === "published" ? "primary" : status === "archived" ? "danger" : "secondary"
              }
            >
              {t.status.move[status]}
            </Button>
          );
        })}
      </div>

      {failure ? (
        <p role="alert" className="max-w-body text-small text-bad">
          {failure}
        </p>
      ) : null}
    </form>
  );
}
