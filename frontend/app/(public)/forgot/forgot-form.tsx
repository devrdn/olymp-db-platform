"use client";

import { useEffect, useRef } from "react";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { InlineLink } from "@/components/ui/inline-link";
import { EMAIL_MAX_CHARS } from "@/lib/auth/signup-rules";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { forgotPasswordAction, type ForgotState } from "./actions";

type Text = Dictionary["auth"]["forgot"];

const FAILURE_ID = "forgot-failure";

/**
 * Asks for a reset link. Once sent, the form gives way to one confirmation,
 * the same whatever was typed.
 */
export function ForgotForm({ t, errors }: { t: Text; errors: Dictionary["errors"] }) {
  return (
    <ActionForm action={forgotPasswordAction} errors={errors} className="flex w-full max-w-96 flex-col gap-7" noValidate>
      {(state) => <Ask t={t} state={state} />}
    </ActionForm>
  );
}

function Ask({ t, state }: { t: Text; state: ForgotState }) {
  if (state.ok) return <Sent t={t} />;
  if (state.unavailable) return <Unavailable t={t} />;

  // Only a malformed request is about what was typed; the throttle and an
  // outage are not.
  const blamesField = state.code === "invalid_request";

  return (
    <>
      <div className="flex flex-col gap-1.5">
        <Field id="login" label={t.login} invalid={blamesField} describedBy={blamesField ? FAILURE_ID : undefined}>
          <Input
            name="login"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            maxLength={EMAIL_MAX_CHARS}
            defaultValue={state.values?.login}
            required
          />
        </Field>

        <ActionOutcome id={FAILURE_ID} />
      </div>

      <ActionButton pendingLabel={t.submitting} className="self-start">
        {t.submit}
      </ActionButton>

      <InlineLink href="/login" className="self-start">
        {t.backToSignIn}
      </InlineLink>
    </>
  );
}

function Sent({ t }: { t: Text }) {
  // The form the visitor was in has gone, so focus moves to what replaced it.
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => heading.current?.focus(), []);

  return (
    <div className="flex max-w-96 flex-col gap-4">
      <h2 ref={heading} tabIndex={-1} className="text-h3 text-ink outline-none">
        {t.sentTitle}
      </h2>
      <p className="text-body text-ink-2">{t.sent}</p>
      <InlineLink href="/login" className="self-start">
        {t.backToSignIn}
      </InlineLink>
    </div>
  );
}

/** Exported for the page, which shows it when the installation says so up front. */
export function Unavailable({ t }: { t: Text }) {
  return (
    <div className="flex max-w-96 flex-col gap-4">
      <p className="text-body text-ink-2">{t.unavailable}</p>
      <InlineLink href="/login" className="self-start">
        {t.backToSignIn}
      </InlineLink>
    </div>
  );
}
