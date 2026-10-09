"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import type { ContestStatus } from "@/lib/api/contests";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { setStatusAction, type StatusState } from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

/**
 * Status transitions as buttons, offered when the mirrored table allows.
 * Publishing may be blocked, with the gate's reasons shown above. Archive uses
 * the danger variant; deletion is never offered here, so it cannot be pressed
 * instead of archive.
 */
export function StatusActions({
  contestId,
  next,
  blocked,
  dict,
}: {
  contestId: string;
  next: readonly ContestStatus[];
  /** Publishing is blocked until the gate passes; the reason is shown above. */
  blocked?: boolean;
  dict: Dictionary;
}) {
  const t = dict.workspace;
  const [state, formAction, pending] = useActionState<StatusState, FormData>(setStatusAction, {});

  const failure = state.code
    ? (messageForCode(state.code, dict.errors))
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
