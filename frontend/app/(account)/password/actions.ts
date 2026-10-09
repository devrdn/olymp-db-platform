"use server";

import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import { failureCode } from "@/lib/api/client";
import { serverRequest } from "@/lib/api/server";
import { checkPasswordChange } from "@/lib/auth/password-change";
import { SESSION_COOKIE } from "@/lib/auth/session";

export type ChangePasswordState = { code?: string };

/**
 * Replaces the issued password. A Server Action: works without JavaScript,
 * keeps the API origin server-side, and gets Next's Origin check.
 *
 * `POST /auth/password` retires every session, this one included, and clears
 * its own cookie; our cookie was set by the sign-in action, so it is deleted
 * here too. Otherwise the guard lets every navigation through and each page
 * fails at the API.
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

  // `.then(null, ...)`, not try/catch: `redirect` throws, and a catch would
  // swallow it.
  const failure = await serverRequest("/auth/password", {
    method: "POST",
    body: check.command,
  }).then(
    () => null,
    (error: unknown) => error,
  );

  if (failure) {
    const code = failureCode(failure);

    // The session expired meanwhile; this is not a password-field error.
    if (code === "unauthenticated") {
      (await cookies()).delete(SESSION_COOKIE);
      redirect("/login");
    }

    return { code };
  }

  (await cookies()).delete(SESSION_COOKIE);

  // The browser arrives with no session, so the address carries the notice.
  redirect("/login?changed=1");
}
