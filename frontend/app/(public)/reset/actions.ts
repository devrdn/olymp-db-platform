"use server";

import { redirect } from "next/navigation";

import { isResetToken } from "@/lib/api/recovery";
import { serverAction, serverRequest, type ActionRefused, type ActionState } from "@/lib/api/server";

/** Only a code: the token and the password never travel back to the browser. */
export type ResetState = ActionState;

/**
 * Sets the new password through the mailed link. The reset ends every session
 * of the account and opens none, so the person signs in with the new password.
 */
export async function resetPasswordAction(_previous: ResetState, form: FormData): Promise<ResetState> {
  const token = String(form.get("token") ?? "");
  const password = String(form.get("password") ?? "");
  const confirm = String(form.get("confirm") ?? "");

  if (!isResetToken(token)) return { code: "reset_link_invalid" };
  // Nothing is trimmed: a space is part of the secret. Strength is the API's.
  if (password !== confirm) return { code: "password_mismatch" };

  const reset = await serverAction(
    () => serverRequest("/public/password/reset", { method: "POST", body: { token, password } }),
    {
      // The routes are not mounted while the installation sends no mail, and
      // then no link can work.
      failure: ({ code }): ActionRefused => ({ code: code === "not_found" ? "reset_link_invalid" : code }),
    },
  );
  if (!reset.ok) return reset;

  // The address carries only the flag that selects the notice.
  redirect("/login?changed=1");
}
