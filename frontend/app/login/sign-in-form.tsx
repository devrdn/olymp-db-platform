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
    <form action={formAction} className="flex w-full max-w-96 flex-col gap-7" noValidate>
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

      {/* The failure hangs off the password field rather than occupying a row.

          It used to hold a blank line whether or not there was a message, to
          keep the submit from moving under the cursor. That cost 28px no other
          step in the form pays — the two fields sat 28px apart and the button
          56px below the second — and the password read as having come loose.

          Taking the line out of the flow instead was worse and measurably so: a
          two-line failure, which Russian reaches at 67 characters in a 384px
          column, printed straight through the button. So the message is in the
          flow and appears only when there is one. At rest the rhythm is even;
          when a sign-in is rejected the form grows by exactly the message and
          nothing is ever overlapped. */}
      <div className="flex flex-col gap-1.5">
        <Field
          id="password"
          label={t.password}
          invalid={Boolean(failure)}
          describedBy={failure ? FAILURE_ID : undefined}
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
