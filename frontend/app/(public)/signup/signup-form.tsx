"use client";

import { useActionState, useState } from "react";

import { Button } from "@/components/ui/button";
import { Field } from "@/components/ui/field";
import { Input } from "@/components/ui/input";
import { InlineLink } from "@/components/ui/inline-link";
import {
  EMAIL_MAX_CHARS,
  FULL_NAME_MAX_BYTES,
  LOGIN_MAX_CHARS,
  RULE_FIELD,
  type BrokenRule,
  type SignupField,
} from "@/lib/auth/signup-rules";
import type { Dictionary } from "@/lib/i18n/dictionary";
import { messageForCode } from "@/lib/i18n/errors";

import { signUpAction, type SignUpState } from "./actions";

type Text = Dictionary["auth"]["signUp"];

/** The one message the blamed fields point at. */
const FAILURE_ID = "sign-up-failure";

/**
 * Which fields an API refusal concerns. `invalid_request` names none of the
 * account fields, so it blames all three; a refusal about the installation
 * blames none.
 */
const BLAMED: Record<string, readonly SignupField[]> = {
  login_taken: ["login"],
  weak_password: ["password"],
  password_mismatch: ["password", "confirm"],
  invalid_request: ["fullName", "login", "email"],
};

/** What each rule the form's own check can find broken says, under its field. */
const RULE_TEXT: Record<BrokenRule, keyof Text> = {
  fullName: "fullNameHint",
  fullNameLength: "fullNameTooLong",
  login: "loginHint",
  passwordLength: "passwordHint",
};

/**
 * The sign-up form; the Server Action works without JavaScript too. The rules
 * stay under their fields from the start, and a rule this side found broken
 * becomes that field's message.
 */
export function SignUpForm({ t, errors }: { t: Text; errors: Dictionary["errors"] }) {
  const [state, formAction, pending] = useActionState<SignUpState, FormData>(signUpAction, {});
  const values = state.values;

  // Controlled, so a capital becomes lowercase as it is typed; the action
  // lowers it again for a browser without JavaScript.
  const [login, setLogin] = useState(values?.login ?? "");
  // Each answer brings back the login as the action read it.
  const [answered, setAnswered] = useState(state);
  if (answered !== state) {
    setAnswered(state);
    if (state.values) setLogin(state.values.login);
  }

  const broken = state.broken;
  const failure = state.code && !broken ? messageForCode(state.code, errors) : null;
  const blamed = failure && state.code ? (BLAMED[state.code] ?? []) : [];

  const wiring = (field: SignupField) =>
    blamed.includes(field) ? { invalid: true, describedBy: FAILURE_ID } : { invalid: false };

  /** A field's rule, as its hint or, once broken, as its message. */
  const rule = (field: SignupField, hint: string) =>
    broken && RULE_FIELD[broken] === field ? { error: t[RULE_TEXT[broken]] } : { hint };

  return (
    <form action={formAction} className="flex w-full max-w-96 flex-col gap-7" noValidate>
      <Field id="full_name" label={t.fullName} {...rule("fullName", t.fullNameHint)} {...wiring("fullName")}>
        <Input
          name="full_name"
          autoComplete="name"
          // A character is at least one byte, so this never cuts off a name that fits.
          maxLength={FULL_NAME_MAX_BYTES}
          defaultValue={values?.fullName}
          required
        />
      </Field>

      <Field id="login" label={t.login} {...rule("login", t.loginHint)} {...wiring("login")}>
        <Input
          name="login"
          autoComplete="username"
          autoCapitalize="none"
          spellCheck={false}
          maxLength={LOGIN_MAX_CHARS}
          value={login}
          onChange={(event) => setLogin(event.target.value.toLowerCase())}
          required
        />
      </Field>

      <Field id="email" label={t.email} hint={t.emailHint} {...wiring("email")}>
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

      <Field id="password" label={t.password} {...rule("password", t.passwordHint)} {...wiring("password")}>
        <Input name="password" type="password" autoComplete="new-password" required />
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

      <p className="text-small text-ink-3">
        {t.haveAccount} <InlineLink href="/login">{t.signIn}</InlineLink>
      </p>
    </form>
  );
}
