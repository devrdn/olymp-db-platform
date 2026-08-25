"use server";

import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import { destinationAfterLogin } from "@/lib/auth/destination";
import { signIn } from "@/lib/auth/sign-in";
import { apiOrigin, fetchIdentity } from "@/lib/auth/session";
export type SignInState = { code?: string };

/**
 * A Server Action rather than a fetch from the browser, for two reasons: the
 * form then works with JavaScript switched off, and the API's origin stays on
 * the server, which matters in development where it is not behind the proxy.
 */
export async function signInAction(
  _previous: SignInState,
  form: FormData,
): Promise<SignInState> {
  const login = String(form.get("login") ?? "").trim();
  const password = String(form.get("password") ?? "");
  if (login === "" || password === "") return { code: "invalid_request" };

  const jar = await cookies();

  const outcome = await signIn(
    { login, password },
    {
      fetchImpl: fetch,
      origin: apiOrigin(),
      setCookie: (cookie) =>
        jar.set(cookie.name, cookie.value, {
          path: cookie.path ?? "/",
          maxAge: cookie.maxAge,
          httpOnly: true,
          sameSite: "lax",
          // Set from this app's own configuration: a browser on plain HTTP
          // discards a Secure cookie, and a local stack has no certificate.
          secure: process.env.NODE_ENV === "production",
        }),
    },
  );

  if (!outcome.ok) return { code: outcome.code };

  const identity = await fetchIdentity();

  // redirect() signals by throwing, so it stays outside any try/catch.
  redirect(
    destinationAfterLogin({
      mustChangePassword: outcome.mustChangePassword,
      permissions: identity?.permissions ?? [],
    }),
  );
}
