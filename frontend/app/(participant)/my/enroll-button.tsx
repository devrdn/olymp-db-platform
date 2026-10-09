"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { enrollAction, type EnrollState } from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

/**
 * The join control in one register row. Each row owns its state, so one
 * pending join does not grey out every button, and a per-contest refusal
 * shows on its own row. The outcome replaces the button, so a live "Join"
 * never invites a second click.
 */
export function EnrollButton({ contestId, dict }: { contestId: string; dict: Dictionary }) {
  const t = dict.participant;
  const [state, formAction, pending] = useActionState<EnrollState, FormData>(enrollAction, {});

  if (state.enrolled) {
    return (
      <span role="status" className="text-small whitespace-nowrap text-good">
        {t.joined}
      </span>
    );
  }

  const failure = state.code
    ? (messageForCode(state.code, dict.errors))
    : null;

  return (
    <form action={formAction} className="flex flex-col items-start gap-1.5">
      <input type="hidden" name="contestId" value={contestId} />

      <Button type="submit" size="sm" variant="secondary" disabled={pending}>
        {pending ? t.joining : t.join}
      </Button>

      {failure ? (
        <p role="alert" className="max-w-48 text-small text-balance text-bad">
          {failure}
        </p>
      ) : null}
    </form>
  );
}
