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
 * Which fields a refusal concerns. Unlike sign-in, this API says which one was
 * wrong, so marking all three would mislead. Every code `POST /auth/password`
 * returns is listed; others get the message alone.
 */
const BLAMED: Record<string, readonly ("current" | "next" | "confirm")[]> = {
  /** Client-side: the confirmation never leaves the browser. */
  password_mismatch: ["next", "confirm"],
  /** The current password is wrong; the new one is fine. */
  wrong_password: ["current"],
  weak_password: ["next"],
  same_password: ["next"],
  /** A field was empty, and the code does not say which. */
  invalid_request: ["current", "next", "confirm"],
};

/**
 * The screen's only client component; it submits without JavaScript too. One
 * message below the last field, pointed at by every field it concerns.
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

  /** A field is described by the message only when it is blamed. */
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

      {/* In the flow: positioned absolutely, a wrapped message would overlap the button. */}
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
