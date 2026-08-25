"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { signInAction, type SignInState } from "./actions";

/** The one message the whole form points at when a sign-in is rejected. */
const FAILURE_ID = "sign-in-failure";

/**
 * The only interactive leaf on this page. The rest of the screen stays a
 * Server Component, and the form still submits with JavaScript switched off
 * because the action is a Server Action.
 *
 * The failure is reported once rather than under whichever field is blamed:
 * the API answers `invalid_credentials` without saying which of the two was
 * wrong — deliberately, so the form cannot be used to find out which logins
 * exist — and putting the message under the password field would claim
 * knowledge the server refused to give.
 *
 * Both fields still carry the invalid state and both point at that one
 * message. Saying it on screen and saying nothing to a screen reader is the
 * same bug as not saying it at all.
 */
export function SignInForm({ dict, next }: { dict: Dictionary; next?: string }) {
  const t = dict.auth.signIn;
  const [state, formAction, pending] = useActionState<SignInState, FormData>(signInAction, {});

  const failure = state.code
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
    : null;

  return (
    <form action={formAction} className="flex w-full max-w-96 flex-col gap-6" noValidate>
      {/* Where the visitor was going before the guard sent them here. It rides
          in the form rather than in the action's closure so the page still
          works with JavaScript switched off, and the action validates it —
          this field is as forgeable as any other. */}
      {next ? <input type="hidden" name="next" value={next} /> : null}

      <Field
        id="login"
        label={t.login}
        invalid={Boolean(failure)}
        describedBy={failure ? FAILURE_ID : undefined}
      >
        <Input name="login" autoComplete="username" required />
      </Field>

      <div className="flex flex-col gap-2">
        <Field
          id="password"
          label={t.password}
          invalid={Boolean(failure)}
          describedBy={failure ? FAILURE_ID : undefined}
        >
          <Input name="password" type="password" autoComplete="current-password" required />
        </Field>

        {/* One line of space is held whether or not there is a message, so the
            common single-line failure never shifts the submit out from under
            the cursor. It sits against the fields rather than floating between
            them and the button, where the reserved space read as a gap
            somebody forgot to close. */}
        <p id={FAILURE_ID} role="alert" className="min-h-5 text-small text-bad">
          {failure}
        </p>
      </div>

      <Button type="submit" disabled={pending} className="self-start">
        {pending ? t.submitting : t.submit}
      </Button>
    </form>
  );
}
