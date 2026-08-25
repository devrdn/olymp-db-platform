"use client";

import { useActionState } from "react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import type { Dictionary } from "@/lib/i18n/dictionary";

import { signInAction, type SignInState } from "./actions";

/**
 * The only interactive leaf on this page. The rest of the screen stays a
 * Server Component, and the form still submits with JavaScript switched off
 * because the action is a Server Action.
 */
export function SignInForm({ dict }: { dict: Dictionary }) {
  const t = dict.auth.signIn;
  const [state, formAction, pending] = useActionState<SignInState, FormData>(signInAction, {});

  const failure = state.code
    ? ((dict.errors as Record<string, string>)[state.code] ?? dict.errors.fallback)
    : null;

  return (
    <form action={formAction} className="flex w-full flex-col gap-5" noValidate>
      <div className="flex flex-col gap-2">
        <Label htmlFor="login">{t.login}</Label>
        <Input
          id="login"
          name="login"
          autoComplete="username"
          required
          aria-invalid={failure ? true : undefined}
          aria-describedby={failure ? "sign-in-error" : undefined}
        />
      </div>

      <div className="flex flex-col gap-2">
        <Label htmlFor="password">{t.password}</Label>
        <Input
          id="password"
          name="password"
          type="password"
          autoComplete="current-password"
          required
          aria-invalid={failure ? true : undefined}
          aria-describedby={failure ? "sign-in-error" : undefined}
        />
        {/* The message sits below the field and holds its place whether or not
            there is one, so an error never shifts the form under the cursor. */}
        <p id="sign-in-error" role="alert" className="min-h-5 text-sm text-bad">
          {failure}
        </p>
      </div>

      <Button type="submit" disabled={pending} className="self-start">
        {pending ? t.submitting : t.submit}
      </Button>
    </form>
  );
}
