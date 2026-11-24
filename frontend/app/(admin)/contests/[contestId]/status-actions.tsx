"use client";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import type { ContestStatus } from "@/lib/api/contests";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { setStatusAction } from "./actions";
import { ReloadWhenChanged } from "./reload-when-changed";

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
  if (next.length === 0) {
    return <p className="text-small text-ink-3">{t.status.terminal}</p>;
  }

  return (
    <ActionForm action={setStatusAction} fields={{ contestId }} errors={dict.errors} className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2.5">
        {next.map((status) => {
          const stopped = status === "published" && blocked;

          return (
            <ActionButton
              key={status}
              name="status"
              value={status}
              disabled={stopped}
              variant={
                status === "published" ? "primary" : status === "archived" ? "danger" : "secondary"
              }
            >
              {t.status.move[status]}
            </ActionButton>
          );
        })}
      </div>

      <ActionOutcome />
      <ReloadWhenChanged label={t.reload} />
    </ActionForm>
  );
}
