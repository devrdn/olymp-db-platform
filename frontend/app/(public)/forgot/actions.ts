"use server";

import { failureCode } from "@/lib/api/client";
import { forgotAnswerSchema } from "@/lib/api/recovery";
import { serverRequest } from "@/lib/api/server";
import { EMAIL_MAX_CHARS } from "@/lib/auth/signup-rules";
import { activeLocale } from "@/lib/i18n/server";

/**
 * `sent` is the one confirmation for every account and for none. A code is
 * only ever a refusal given before anything was looked up (the throttle, a
 * malformed request, an unreachable API), so it says nothing about an account
 * either; `unavailable` means the installation offers no recovery.
 */
export type ForgotState = { sent?: boolean; unavailable?: boolean; code?: string; values?: { login: string } };

/** Asks for a reset link, mailed in the reader's language. */
export async function forgotPasswordAction(_previous: ForgotState, form: FormData): Promise<ForgotState> {
  const login = String(form.get("login") ?? "").trim();
  const values = { login };

  // The API bounds a login or an email alike, by the email's bound.
  if (login === "" || login.length > EMAIL_MAX_CHARS) return { code: "invalid_request", values };

  try {
    forgotAnswerSchema.parse(
      await serverRequest(`/public/password/forgot?lang=${encodeURIComponent(await activeLocale())}`, {
        method: "POST",
        body: { login },
      }),
    );
    return { sent: true };
  } catch (error) {
    const code = failureCode(error);
    // The routes are not mounted while the installation sends no mail.
    if (code === "not_found") return { unavailable: true };
    return { code, values };
  }
}
