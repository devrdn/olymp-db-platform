import { cookies } from "next/headers";

import { cookieSecure } from "./cookie-policy";
import { EMAIL_MAX_CHARS } from "./signup-rules";

/**
 * The address a sign-up's code went to, kept by this server between the
 * sign-up form and the confirmation page so it never sits in the address
 * bar. It is this frontend's own cookie; the API never sees it.
 */

/** Only the sign-up pages and their actions read it. */
const PATH = "/signup";

/** `signup.CodeLifetime`: the address is worth keeping as long as a code confirms. */
const MAX_AGE_SECONDS = 30 * 60;

/**
 * `__Secure-`, not the sign-up cookie's `__Host-`: a browser refuses a
 * `__Host-` cookie whose path is not `/`, and this one is kept to /signup.
 */
export function signupEmailCookieName(): string {
  return cookieSecure() ? "__Secure-dbcontest_signup_email" : "dbcontest_signup_email";
}

/** Sets it afresh, for a new code's whole window. */
export async function rememberSignupEmail(email: string): Promise<void> {
  (await cookies()).set(signupEmailCookieName(), email, {
    path: PATH,
    maxAge: MAX_AGE_SECONDS,
    httpOnly: true,
    sameSite: "lax",
    secure: cookieSecure(),
  });
}

/** The address, or undefined when the browser holds none a sign-up could have written. */
export async function signupEmail(): Promise<string | undefined> {
  const value = (await cookies()).get(signupEmailCookieName())?.value;
  return value && value.length <= EMAIL_MAX_CHARS && value.includes("@") ? value : undefined;
}

export async function forgetSignupEmail(): Promise<void> {
  (await cookies()).delete({ name: signupEmailCookieName(), path: PATH, secure: cookieSecure() });
}
