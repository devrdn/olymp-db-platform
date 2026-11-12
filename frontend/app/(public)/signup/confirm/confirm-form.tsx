"use client";

import { useActionState, useEffect, useState, useTransition } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { InlineLink } from "@/components/ui/inline-link";
import { RESEND_COOLDOWN_SECONDS } from "@/lib/auth/signup-rules";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";

import { confirmSignupAction, resendCodeAction, type ConfirmState, type ResendOutcome } from "./actions";

type Text = Dictionary["auth"]["confirm"];

/** The one message the blamed fields point at. */
const FAILURE_ID = "confirm-failure";

/** The refusals about what was typed; the others are about the sign-up itself. */
const BLAMES_CODE = new Set(["signup_wrong_code", "invalid_request"]);

/** An expired sign-up cannot be revived, and a missing one was never here: the way on is a new one. */
const START_AGAIN = "signup_code_expired";

/**
 * The code form, and below it a new code once the cooldown has passed. The
 * address is the server's, read from its cookie, so nothing here names it.
 * What it says never depends on whether the address has an account: the API
 * answers alike, and so does this.
 */
export function ConfirmForm({ t, errors }: { t: Text; errors: Dictionary["errors"] }) {
  const [state, formAction, pending] = useActionState<ConfirmState, FormData>(confirmSignupAction, {});

  const failure = state.code ? messageForCode(state.code, errors) : null;
  const blamesCode = Boolean(state.code && BLAMES_CODE.has(state.code));

  return (
    <div className="flex w-full max-w-96 flex-col gap-10">
      <form action={formAction} className="flex flex-col gap-7" noValidate>
        {/* In the flow: positioned absolutely, a wrapped message would overlap the button. */}
        <div className="flex flex-col gap-1.5">
          <Field
            id="code"
            label={t.code}
            hint={t.codeHint}
            invalid={blamesCode}
            describedBy={blamesCode ? FAILURE_ID : undefined}
          >
            <Input
              name="code"
              autoComplete="one-time-code"
              inputMode="numeric"
              className="font-mono"
              required
            />
          </Field>

          {failure ? (
            <p id={FAILURE_ID} role="alert" className="text-small text-bad">
              {failure}
            </p>
          ) : null}

          {state.code === START_AGAIN ? <StartOver t={t} /> : null}
        </div>

        <Button type="submit" disabled={pending} className="self-start">
          {pending ? t.submitting : t.submit}
        </Button>
      </form>

      <Resend t={t} errors={errors} />
    </div>
  );
}

/**
 * A new code, offered once `RESEND_COOLDOWN_SECONDS` have passed since the
 * last one went out, or as long as the API says when it refuses one as too
 * soon. Counted against the clock rather than by ticks, so a throttled
 * background tab does not stretch the wait.
 */
function Resend({ t, errors }: { t: Text; errors: Dictionary["errors"] }) {
  const [until, setUntil] = useState(() => Date.now() + RESEND_COOLDOWN_SECONDS * 1000);
  const [now, setNow] = useState(() => Date.now());
  const [outcome, setOutcome] = useState<ResendOutcome | null>(null);
  const [sending, startSending] = useTransition();

  const left = Math.max(0, Math.ceil((until - now) / 1000));
  const waiting = left > 0;

  useEffect(() => {
    if (!waiting) return;
    const tick = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(tick);
  }, [waiting, until]);

  const wait = (seconds: number) => {
    const at = Date.now();
    setNow(at);
    setUntil(at + seconds * 1000);
  };

  const resend = () => {
    startSending(async () => {
      const answer = await resendCodeAction();
      setOutcome(answer);
      if (answer.sent) wait(RESEND_COOLDOWN_SECONDS);
      else if (answer.code === "signup_too_soon") wait(answer.retryAfterSeconds ?? RESEND_COOLDOWN_SECONDS);
    });
  };

  return (
    <div className="flex flex-col gap-3 border-t border-line pt-6">
      <Button type="button" variant="secondary" disabled={waiting || sending} onClick={resend} className="self-start">
        {sending ? t.resending : waiting ? t.resendIn.replace("{seconds}", String(left)) : t.resend}
      </Button>

      {outcome?.sent ? (
        <p role="status" className="text-small text-ink-2">
          {t.resent}
        </p>
      ) : outcome ? (
        <>
          <p role="alert" className="text-small text-bad">
            {messageForCode(outcome.code, errors)}
          </p>
          {outcome.code === START_AGAIN ? <StartOver t={t} /> : null}
        </>
      ) : null}

      <p className="text-small text-ink-3">{t.note}</p>
    </div>
  );
}

/** Exported for the page, which offers it when there is nothing to confirm. */
export function StartOver({ t }: { t: Text }) {
  return (
    <InlineLink href="/signup" className="self-start">
      {t.startOver}
    </InlineLink>
  );
}
