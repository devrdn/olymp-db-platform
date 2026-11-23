"use client";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import type { MailStatus } from "@/lib/api/settings";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { fill } from "@/lib/i18n/fill";

import { sendTestMailAction } from "./actions";

/**
 * Whether the installation sends email, and a test message to the
 * administrator's own address to prove it, answered with the relay's reply.
 */
export function MailTest({ mail, dict }: { mail: MailStatus; dict: Dictionary }) {
  const t = dict.settings.mail;

  return (
    <section className="flex max-w-narrative flex-col gap-4 border-t border-line pt-6">
      <div className="flex flex-col gap-1.5">
        <h2 className="text-h3 text-ink">{t.heading}</h2>
        <p className="max-w-body text-body text-ink-2">
          {mail.enabled ? fill(t.on, { address: mail.from }) : t.off}
        </p>
      </div>

      {mail.enabled ? (
        <ActionForm action={sendTestMailAction} errors={dict.errors} className="flex flex-col gap-3">
          {(state) => (
            <>
              <p className="max-w-body text-small text-ink-3">{t.hint}</p>

              <div className="flex flex-wrap items-center gap-4">
                <ActionButton variant="secondary" pendingLabel={t.sending}>
                  {t.send}
                </ActionButton>

                <ActionOutcome saved={state.sentTo ? fill(t.sent, { address: state.sentTo }) : undefined}>
                  {/* The relay's own words: what an operator searches for. */}
                  {state.subject ? <p className="font-mono text-ink-2">{state.subject}</p> : null}
                </ActionOutcome>
              </div>
            </>
          )}
        </ActionForm>
      ) : null}
    </section>
  );
}
