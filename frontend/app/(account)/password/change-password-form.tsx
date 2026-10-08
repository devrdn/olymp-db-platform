"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { changePasswordAction, type ChangePasswordState } from "./actions";
import { messageForCode } from "@/lib/i18n/errors";

/** The one message the blamed fields point at. */
const FAILURE_ID = "change-password-failure";

/**
 * Which fields a refusal is about.
 *
 * Sign-in marks both of its fields, because the API deliberately refuses to
 * say which one was wrong. Here it does say, and marking all three would throw
 * that away: an author told `wrong_password` and shown three red borders
 * re-types the new password, which was never the problem. Every code the
 * server can answer from `POST /auth/password` is listed; anything else — a
 * gateway failure, an internal error — belongs to no field and gets the
 * message alone.
 */
const BLAMED: Record<string, readonly ("current" | "next" | "confirm")[]> = {
  /** Ours: the confirmation never leaves the browser. */
  password_mismatch: ["next", "confirm"],
  /** The handover secret itself is wrong. Nothing about the new one is. */
  wrong_password: ["current"],
  weak_password: ["next"],
  same_password: ["next"],
  /** A field was left empty, and the form cannot tell which from here. */
  invalid_request: ["current", "next", "confirm"],
};

/**
 * The only interactive leaf on the screen; the rest stays a Server Component
 * and the form still submits with JavaScript switched off.
 *
 * The failure is written once, below the last field, and every field it
 * concerns points at it. Saying it on screen and saying nothing to a screen
 * reader is the same bug as not saying it at all.
 */
export function ChangePasswordForm({ dict }: { dict: Dictionary }) {
  const t = dict.auth.changePassword;
  const [state, formAction, pending] = useActionState<ChangePasswordState, FormData>(
    changePasswordAction,
    {},
  );

  const failure = state.code
    ? (messageForCode(state.code, dict.errors))
    : null;
  const blamed = state.code ? (BLAMED[state.code] ?? []) : [];

  /** A field is only described by the message when the message is about it. */
  const wiring = (field: "current" | "next" | "confirm") =>
    blamed.includes(field)
      ? { invalid: true, describedBy: FAILURE_ID }
      : { invalid: false, describedBy: undefined };

  return (
    <form action={formAction} className="flex w-full max-w-96 flex-col gap-7" noValidate>
      <Field id="current" label={t.current} {...wiring("current")}>
        <Input name="current" type="password" autoComplete="current-password" required />
      </Field>

      <Field id="next" label={t.next} {...wiring("next")}>
        <Input name="next" type="password" autoComplete="new-password" required />
      </Field>

      {/* The message sits in the flow rather than out of it. Absolutely
          positioned, a two-line failure — which Russian reaches inside this
          384px column — prints straight through the button below it. In the
          flow the form grows by exactly the message and nothing is overlapped;
          at rest the rhythm is even, because there is no reserved blank line. */}
      <div className="flex flex-col gap-1.5">
        <Field id="confirm" label={t.confirm} {...wiring("confirm")}>
          <Input name="confirm" type="password" autoComplete="new-password" required />
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
