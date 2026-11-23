"use client";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import { InlineLink } from "@/components/ui/inline-link";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { enrollAction, type EnrollState } from "./actions";
import { fill } from "@/lib/i18n/fill";

/**
 * The join control in one register row. Each row owns its state, so one
 * pending join does not grey out every button, and a per-contest refusal
 * shows on its own row. The outcome replaces the button, so a live "Join"
 * never invites a second click.
 */
export function EnrollButton({ contestId, dict }: { contestId: string; dict: Pick<Dictionary, "participant" | "errors"> }) {
  return (
    <ActionForm
      action={enrollAction}
      fields={{ contestId }}
      errors={dict.errors}
      className="flex flex-col items-start gap-1.5"
    >
      {(state) => <Join state={state} dict={dict} />}
    </ActionForm>
  );
}

function Join({ state, dict }: { state: EnrollState; dict: Pick<Dictionary, "participant" | "errors"> }) {
  const t = dict.participant;

  if (state.ok) {
    return (
      <span role="status" className="text-small whitespace-nowrap text-good">
        {t.joined}
      </span>
    );
  }

  // Only this refusal is about the domain.
  const domain = state.code === "contest_trusted_email_required" ? state.domain : undefined;
  const accepted = domain ? fill(t.trustedDomain, { domain }) : null;
  // A staff-made account may hold such an address unconfirmed; following a
  // reset link confirms it.
  const confirmByReset = state.code === "contest_trusted_email_required" && state.confirmByReset;

  return (
    <>
      <ActionButton size="sm" variant="secondary" pendingLabel={t.joining}>
        {t.join}
      </ActionButton>

      <ActionOutcome narrow>
        {accepted || confirmByReset ? (
          <>
            {accepted ? <p className="text-ink-2">{accepted}</p> : null}
            {confirmByReset ? (
              <p>
                <InlineLink href="/forgot">{t.trustedDomainReset}</InlineLink>
              </p>
            ) : null}
          </>
        ) : null}
      </ActionOutcome>
    </>
  );
}
