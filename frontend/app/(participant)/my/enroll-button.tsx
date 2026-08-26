"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { enrollAction, type EnrollState } from "./actions";

/**
 * The one interactive cell in the participant's register.
 *
 * A component per row rather than one state for the whole table: two students
 * sitting next to each other join different contests, and a shared pending
 * flag would grey out every button on the page while one of them resolves.
 * Each button also carries its own failure, which is the only place a
 * per-contest refusal — a closed enrolment, an address outside the university
 * network — can honestly be shown.
 *
 * The outcome replaces the button rather than sitting beside it. Leaving a
 * live "Join" under the sentence "you are enrolled" invites the second click
 * that produces the first confusing error.
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
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
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
