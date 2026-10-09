"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { signInAction, type SignInState } from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

/** The one message both fields point at on rejection. */
const FAILURE_ID = "sign-in-failure";

/**
 * The page's only client component; the Server Action still works without
 * JavaScript. The API does not say which field was wrong
 * (`invalid_credentials`), so logins cannot be probed; one message is shown and
 * both fields point at it as invalid.
 */
export function SignInForm({ dict, next }: { dict: Dictionary; next?: string }) {
  const t = dict.auth.signIn;
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
      </div>

      <Button type="submit" disabled={pending} className="self-start">
        {pending ? t.submitting : t.submit}
      </Button>
    </form>
  );
}
