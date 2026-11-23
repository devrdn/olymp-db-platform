"use client";

import { ActionButton, ActionForm, ActionOutcome } from "@/components/product/action-form";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { InlineLink } from "@/components/ui/inline-link";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { labelFor } from "@/lib/i18n/label";

import { resetPasswordAction } from "./actions";

type Text = Dictionary["auth"]["reset"];

const FAILURE_ID = "reset-failure";

/** Which fields a refusal concerns; a refusal about the link blames none. */
const BLAMED: Record<string, readonly ("next" | "confirm")[]> = {
  weak_password: ["next"],
  password_mismatch: ["next", "confirm"],
};

/**
 * The new password, twice, under the rule sign-up states. The link's token
 * rides in a hidden field, never shown; a link that can no longer be used
 * offers a new one.
 */
export function ResetForm({
  t,
  passwordHint,
  errors,
  token,
}: {
  t: Text;
  passwordHint: string;
  errors: Dictionary["errors"];
  token: string;
}) {
  return (
    <ActionForm
      action={resetPasswordAction}
      fields={{ token }}
      errors={errors}
      className="flex w-full max-w-96 flex-col gap-7"
      noValidate
    >
      {(state) => <NewPassword t={t} passwordHint={passwordHint} code={state.code} />}
    </ActionForm>
  );
}

function NewPassword({ t, passwordHint, code }: { t: Text; passwordHint: string; code?: string }) {
  const blamed = code ? (labelFor(BLAMED, code) ?? []) : [];
  const wiring = (field: "next" | "confirm") =>
    blamed.includes(field) ? { invalid: true, describedBy: FAILURE_ID } : { invalid: false };

  return (
    <>
      <Field id="password" label={t.next} hint={passwordHint} {...wiring("next")}>
        <Input name="password" type="password" autoComplete="new-password" required />
      </Field>

      {/* In the flow: positioned absolutely, a wrapped message would overlap the button. */}
      <div className="flex flex-col gap-1.5">
        <Field id="confirm" label={t.confirm} {...wiring("confirm")}>
          <Input name="confirm" type="password" autoComplete="new-password" required />
        </Field>

        <ActionOutcome id={FAILURE_ID} />

        {code === "reset_link_invalid" ? (
          <InlineLink href="/forgot" className="self-start">
            {t.requestNew}
          </InlineLink>
        ) : null}
      </div>

      <ActionButton pendingLabel={t.submitting} className="self-start">
        {t.submit}
      </ActionButton>
    </>
  );
}
