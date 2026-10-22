"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import type { MailStatus } from "@/lib/api/settings";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";

import { sendTestMailAction, type MailTestState } from "./actions";

/**
 * Whether the installation sends email, and a test message to the
 * administrator's own address to prove it, answered with the relay's reply.
 */
export function MailTest({ mail, dict }: { mail: MailStatus; dict: Dictionary }) {
  const t = dict.settings.mail;
  const [state, send, pending] = useActionState<MailTestState, FormData>(sendTestMailAction, {});

  const failure = state.code ? messageForCode(state.code, dict.errors) : null;

  return (
    <section className="flex max-w-narrative flex-col gap-4 border-t border-line pt-6">
      <div className="flex flex-col gap-1.5">
        <h2 className="text-h3 text-ink">{t.heading}</h2>
        <p className="max-w-body text-body text-ink-2">
          {mail.enabled ? t.on.replace("{address}", mail.from) : t.off}
        </p>
      </div>

      {mail.enabled ? (
        <form action={send} className="flex flex-col gap-3">
          <p className="max-w-body text-small text-ink-3">{t.hint}</p>

          <div className="flex flex-wrap items-center gap-4">
            <Button type="submit" variant="secondary" disabled={pending}>
              {pending ? t.sending : t.send}
            </Button>

            {state.sentTo && !failure ? (
              <p role="status" className="text-small text-good">
                {t.sent.replace("{address}", state.sentTo)}
              </p>
            ) : null}

            {failure ? (
              <p role="alert" className="max-w-body text-small text-bad">
                {failure}
                {/* The relay's own words: what an operator searches for. */}
                {state.subject ? <span className="ml-1 font-mono text-ink-2">{state.subject}</span> : null}
              </p>
            ) : null}
          </div>
        </form>
      ) : null}
    </section>
  );
}
