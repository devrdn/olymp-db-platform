"use client";

import Link from "next/link";
import { useActionState, useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import type { RegistrationOutcome } from "@/lib/api/registrations";
import { displayCode, EMAIL_MAX_CHARS, NAME_MAX_CHARS } from "@/lib/api/registrations-terms";
import { copyText } from "@/lib/clipboard";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";

import { registerAction, type RegisterState } from "./actions";

type Text = Dictionary["registration"];

/** The one message the refused fields point at. */
const FAILURE_ID = "register-failure";

/**
 * The registration form and, once the API has answered, what it answered in
 * its place. The Server Action still works without JavaScript.
 */
export function RegisterForm({ t, errors }: { t: Text; errors: Dictionary["errors"] }) {
  const [state, formAction, pending] = useActionState<RegisterState, FormData>(registerAction, {});

  if (state.outcome) return <Outcome outcome={state.outcome} t={t} />;

  const failure = state.code ? messageForCode(state.code, errors) : null;
  const blamesNames = state.code === "registration_invalid_name";
  const blamesEmail = state.code === "registration_invalid_email";
  const values = state.values;

  return (
    <form action={formAction} className="flex w-full max-w-96 flex-col gap-7" noValidate>
      <Field
        id="first_name"
        label={t.firstName}
        invalid={blamesNames}
        describedBy={blamesNames ? FAILURE_ID : undefined}
      >
        <Input
          name="first_name"
          autoComplete="given-name"
          maxLength={NAME_MAX_CHARS}
          defaultValue={values?.firstName}
          required
        />
      </Field>

      <Field
        id="last_name"
        label={t.lastName}
        invalid={blamesNames}
        describedBy={blamesNames ? FAILURE_ID : undefined}
      >
        <Input
          name="last_name"
          autoComplete="family-name"
          maxLength={NAME_MAX_CHARS}
          defaultValue={values?.lastName}
          required
        />
      </Field>

      <div className="flex flex-col gap-1.5">
        <Field
          id="email"
          label={t.email}
          hint={t.emailHint}
          invalid={blamesEmail}
          describedBy={blamesEmail ? FAILURE_ID : undefined}
        >
          <Input
            name="email"
            type="email"
            autoComplete="email"
            inputMode="email"
            maxLength={EMAIL_MAX_CHARS}
            defaultValue={values?.email}
            required
          />
        </Field>

        {failure ? (
          <p id={FAILURE_ID} role="alert" className="text-small text-bad">
            {failure}
          </p>
        ) : null}
      </div>

      <Button type="submit" disabled={pending} className="self-start">
        {pending ? t.submitting : t.submit}
      </Button>
    </form>
  );
}

function Outcome({ outcome, t }: { outcome: RegistrationOutcome; t: Text }) {
  // The form the visitor was in has gone, so focus moves to what replaced it.
  const heading = useRef<HTMLHeadingElement>(null);
  useEffect(() => heading.current?.focus(), []);

  if (outcome.status === "existing") {
    return (
      <div className="flex max-w-lede flex-col gap-4">
        <h2 ref={heading} tabIndex={-1} className="text-h3 text-ink outline-none">
          {t.repeat.title}
        </h2>
        <p className="text-body text-ink-2">
          {(outcome.emailSent ? t.repeat.emailSent : t.repeat.emailNotSent).replace("{email}", outcome.email)}
        </p>
      </div>
    );
  }

  const c = t.created;
  return (
    <div className="flex max-w-lede flex-col gap-8">
      <div className="flex flex-col gap-4">
        <h2 ref={heading} tabIndex={-1} className="text-h3 text-ink outline-none">
          {c.title}
        </h2>

        <div className="flex flex-col gap-2 border-l-2 border-good pl-4">
          <span className="text-label text-ink-3 uppercase">{c.codeLabel}</span>
          <div className="flex flex-wrap items-center gap-4">
            <span className="font-mono text-h2 text-ink select-all">{displayCode(outcome.code)}</span>
            <CopyButton value={displayCode(outcome.code)} label={c.copyCode} t={t} />
          </div>
          <p className="text-body text-ink-2">{c.codeHint}</p>
        </div>

        <p className={outcome.emailSent ? "text-body text-ink-2" : "text-body text-warn"}>
          {outcome.emailSent ? c.emailSent.replace("{email}", outcome.email) : c.emailNotSent}
        </p>
      </div>

      {outcome.account === "created" && outcome.login && outcome.oneTimePassword ? (
        <section className="flex flex-col gap-4">
          <h3 className="text-h3 text-ink">{c.accountTitle}</h3>
          <dl className="flex flex-col gap-3 border border-warn bg-warn-wash p-4">
            <Secret term={c.login} value={outcome.login} copyLabel={c.copyLogin} t={t} />
            <Secret term={c.password} value={outcome.oneTimePassword} copyLabel={c.copyPassword} t={t} />
          </dl>
          <p className="text-body text-ink-2">{c.passwordNote}</p>
          <SignInLink t={t} />
        </section>
      ) : (
        <section className="flex flex-col gap-4">
          <p className="text-body text-ink-2">{c.accountExisting}</p>
          <SignInLink t={t} />
        </section>
      )}
    </div>
  );
}

function Secret({ term, value, copyLabel, t }: { term: string; value: string; copyLabel: string; t: Text }) {
  return (
    <div className="flex flex-wrap items-center justify-between gap-3">
      <div className="flex min-w-0 flex-col gap-1">
        <dt className="text-data text-ink-3">{term}</dt>
        <dd className="font-mono text-row break-all text-ink select-all">{value}</dd>
      </div>
      <CopyButton value={value} label={copyLabel} t={t} />
    </div>
  );
}

/** Says what it copies in its name, since three of them share one visible word. */
function CopyButton({ value, label, t }: { value: string; label: string; t: Text }) {
  const [copied, setCopied] = useState(false);
  return (
    <Button
      type="button"
      variant="secondary"
      size="sm"
      aria-label={copied ? `${label}: ${t.copied}` : label}
      onClick={async () => setCopied(await copyText(value))}
    >
      {copied ? t.copied : t.copy}
    </Button>
  );
}

function SignInLink({ t }: { t: Text }) {
  return (
    <Link
      href="/login"
      className="inline-flex h-(--control-h) items-center self-start rounded-full bg-cta px-4 text-control font-semibold text-cta-fg hover:opacity-88"
    >
      {t.signIn}
    </Link>
  );
}
