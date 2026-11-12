"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import { InlineLink } from "@/components/ui/inline-link";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { enrollAction, type EnrollState } from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

/**
 * The join control in one register row. Each row owns its state, so one
 * pending join does not grey out every button, and a per-contest refusal
 * shows on its own row. The outcome replaces the button, so a live "Join"
 * never invites a second click.
 */
export function EnrollButton({ contestId, dict }: { contestId: string; dict: Pick<Dictionary, "participant" | "errors"> }) {
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
  // Only this refusal is about the domain. A replacer function, so a `$` in
  // the domain is not read as a pattern.
  const domain = state.code === "contest_trusted_email_required" ? state.domain : undefined;
  const accepted = domain ? t.trustedDomain.replace("{domain}", () => domain) : null;
  // A staff-made account may hold such an address unconfirmed; following a
  // reset link confirms it.
  const confirmByReset = state.code === "contest_trusted_email_required" && state.confirmByReset;

  return (
    <form action={formAction} className="flex flex-col items-start gap-1.5">
      <input type="hidden" name="contestId" value={contestId} />

      <Button type="submit" size="sm" variant="secondary" disabled={pending}>
        {pending ? t.joining : t.join}
      </Button>

      {failure ? (
        <div role="alert" className="flex max-w-48 flex-col gap-1 text-small text-balance">
          <p className="text-bad">{failure}</p>
          {accepted ? <p className="text-ink-2">{accepted}</p> : null}
          {confirmByReset ? (
            <p>
              <InlineLink href="/forgot">{t.trustedDomainReset}</InlineLink>
            </p>
          ) : null}
        </div>
      ) : null}
    </form>
  );
}
