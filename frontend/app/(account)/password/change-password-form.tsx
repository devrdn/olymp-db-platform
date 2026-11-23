"use client";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { labelFor } from "@/lib/i18n/label";

import { changePasswordAction } from "./actions";

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

  return (
    <ActionForm action={changePasswordAction} errors={dict.errors} className="flex w-full max-w-96 flex-col gap-7" noValidate>
      {(state) => {
        const blamed = state.code ? (labelFor(BLAMED, state.code) ?? []) : [];
        /** A field is described by the message only when it is blamed. */
        const wiring = (field: "current" | "next" | "confirm") =>
          blamed.includes(field)
            ? { invalid: true, describedBy: FAILURE_ID }
            : { invalid: false, describedBy: undefined };

        return (
          <>
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

              <ActionOutcome id={FAILURE_ID} />
            </div>

            <ActionButton pendingLabel={t.submitting} className="self-start">
              {t.submit}
            </ActionButton>
          </>
        );
      }}
    </ActionForm>
  );
}
