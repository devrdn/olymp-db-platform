"use server";

import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import { ApiError } from "@/lib/api/client";
import { serverRequest } from "@/lib/api/server";
import { checkPasswordChange } from "@/lib/auth/password-change";
import { SESSION_COOKIE } from "@/lib/auth/session";

export type ChangePasswordState = { code?: string };

/**
 * Replacing the password an administrator handed over.
 *
 * A Server Action for the same two reasons sign-in is one: the form works with
 * JavaScript switched off, and the API's origin never reaches the browser.
 * Next checks the request's Origin against its Host before an action runs, so
 * the cross-site post this form would otherwise invite is refused before any
 * of this code executes.
 *
 * The exchange ends with the session gone. `POST /auth/password` retires every
 * session this account holds — including the one that made the request — and
 * clears its own cookie on the way out. Our copy of that cookie was written by
 * the sign-in action and the API cannot reach it, so it is deleted here. Skip
 * that and the browser keeps presenting an identifier the server has already
 * forgotten: the guard waves every navigation through and each page dies at
 * the API instead, which is the confusing version of being logged out.
 */
export async function changePasswordAction(
  _previous: ChangePasswordState,
  form: FormData,
): Promise<ChangePasswordState> {
  const check = checkPasswordChange({
    current: String(form.get("current") ?? ""),
    next: String(form.get("next") ?? ""),
    confirm: String(form.get("confirm") ?? ""),
  });

  if (!check.ok) return { code: check.code };

  // `.then(null, error)` rather than try/catch: `redirect` signals by throwing,
  // and a catch around it would swallow the redirect along with the failure.
  const failure = await serverRequest("/auth/password", {
    method: "POST",
    body: check.command,
  }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) {
    const code = failure instanceof ApiError ? failure.code : "unreachable";

    // The session died while the form was open. There is nothing to change any
    // more, and reporting it under a password field would be misleading.
    if (code === "unauthenticated") {
      (await cookies()).delete(SESSION_COOKIE);
      redirect("/login");
    }

    return { code };
  }

  (await cookies()).delete(SESSION_COOKIE);

  // Not a flag the form could set: the browser is arriving at a fresh page with
  // no session, so the only way to say what just happened is in the address.
  redirect("/login?changed=1");
}
