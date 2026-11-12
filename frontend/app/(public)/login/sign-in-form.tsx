"use client";

import { useActionState } from "react";

import { Button, buttonVariants } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { InlineLink } from "@/components/ui/inline-link";
import { microsoftStartHref } from "@/lib/auth/microsoft";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";

import { signInAction, type SignInState } from "./actions";
import type { SignInErrors } from "./codes";

/** The one message both fields point at on rejection. */
const FAILURE_ID = "sign-in-failure";

/** The other ways in the installation offers; each link shows only when it does. */
export type SignInOffers = { signUp: boolean; forgot: boolean; microsoft: boolean };

const NO_OFFERS: SignInOffers = { signUp: false, forgot: false, microsoft: false };

/**
 * The page's only client component; the Server Action still works without
 * JavaScript. The API does not say which field was wrong
 * (`invalid_credentials`), so logins cannot be probed; one message is shown and
 * both fields point at it as invalid.
 */
export function SignInForm({
  dict,
  next,
  offers = NO_OFFERS,
}: {
  dict: { signIn: Dictionary["auth"]["signIn"]; errors: SignInErrors };
  next?: string;
  offers?: SignInOffers;
}) {
  const t = dict.signIn;
  const [state, formAction, pending] = useActionState<SignInState, FormData>(signInAction, {});

  const failure = state.code
    ? (messageForCode(state.code, dict.errors))
    : null;
  // A busy server says nothing about the input, so the fields are not marked
  // invalid.
  const blamesFields = Boolean(failure) && state.code !== "sign_in_busy";

  return (
    <form action={formAction} className="flex w-full max-w-96 flex-col gap-7" noValidate>
      {/* Where the visitor was headed. In the form so it works without
         JavaScript; the action validates it, since it is forgeable. */}
      {next ? <input type="hidden" name="next" value={next} /> : null}

      <Field
        id="login"
        label={t.login}
        invalid={blamesFields}
        describedBy={blamesFields ? FAILURE_ID : undefined}
      >
        <Input name="login" autoComplete="username" required />
      </Field>

      {/* The message is in the flow and appears only when there is one: a
         reserved blank line spaced the form unevenly, and an out-of-flow
         message overlapped the button when it wrapped. */}
      <div className="flex flex-col gap-1.5">
        <Field
          id="password"
          label={t.password}
          invalid={blamesFields}
          describedBy={blamesFields ? FAILURE_ID : undefined}
        >
          <Input name="password" type="password" autoComplete="current-password" required />
        </Field>

        {failure ? (
          <p id={FAILURE_ID} role="alert" className="text-small text-bad">
            {failure}
          </p>
        ) : null}

        {offers.forgot ? (
          <InlineLink href="/forgot" className="self-start">
            {t.forgot}
          </InlineLink>
        ) : null}
      </div>

      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit" disabled={pending}>
          {pending ? t.submitting : t.submit}
        </Button>

        {/* A plain link, not `next/link`: it leaves the app for the API and
           then for Microsoft, and must not be prefetched. */}
        {offers.microsoft ? (
          <a href={microsoftStartHref(next)} className={buttonVariants({ variant: "secondary" })}>
            {t.microsoft}
          </a>
        ) : null}
      </div>

      {/* Other ways in go below the form. */}
      {offers.signUp ? (
        <p className="text-small text-ink-3">
          {t.noAccount} <InlineLink href="/signup">{t.signUp}</InlineLink>
        </p>
      ) : null}
    </form>
  );
}
