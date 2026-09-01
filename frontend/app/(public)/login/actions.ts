"use server";

import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import { callerHeaders } from "@/lib/api/caller";
import { cookieSecure } from "@/lib/auth/cookie-policy";
import { destinationAfterLogin } from "@/lib/auth/destination";
import { signIn } from "@/lib/auth/sign-in";
import { apiOrigin } from "@/lib/api/config";
import { fetchIdentity } from "@/lib/auth/session";
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

  // Where the guard was taking them before it stopped here. It arrives from
  // the browser, so `destinationAfterLogin` is the one that decides whether it
  // is a path on this origin at all.
  const next = form.get("next");

  const jar = await cookies();

  const outcome = await signIn(
    { login, password },
    {
      fetchImpl: fetch,
      origin: apiOrigin(),
      // The browser's own address, handed on: sign-in leaves from this
      // server, and without the chain the API's per-address throttle counts
      // every student in the building as one machine.
      headers: await callerHeaders(),
      setCookie: (cookie) =>
        jar.set(cookie.name, cookie.value, {
          path: cookie.path ?? "/",
          maxAge: cookie.maxAge,
          httpOnly: true,
          sameSite: "lax",
          // From the deployment, not from NODE_ENV. A production build served
          // over plain http — `make front-start`, a box behind no proxy — used
          // to mark this Secure, and the browser then discarded it: signing in
          // appeared to work and the next click went back to the form.
          secure: cookieSecure(),
        }),
    },
  );

  if (!outcome.ok) return { code: outcome.code };

  // The session exists either way — it was just issued. If the API cannot be
  // asked what it may do, the safe landing is the participant's own screen,
  // which every account can open.
  const identity = await fetchIdentity().catch(() => null);

  // redirect() signals by throwing, so it stays outside any try/catch.
  redirect(
    destinationAfterLogin(
      {
        mustChangePassword: outcome.mustChangePassword,
        permissions: identity?.permissions ?? [],
      },
      typeof next === "string" ? next : undefined,
    ),
  );
}
