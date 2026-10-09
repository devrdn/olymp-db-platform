"use server";

import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import { callerHeaders } from "@/lib/api/caller";
import { cookieSecure } from "@/lib/auth/cookie-policy";
import { destinationAfterLogin } from "@/lib/auth/destination";
import { DEVICE_COOKIE, signIn } from "@/lib/auth/sign-in";
import { apiOrigin } from "@/lib/api/config";
import { fetchIdentity } from "@/lib/auth/session";
export type SignInState = { code?: string };

/**
 * A Server Action: works without JavaScript and keeps the API origin on the
 * server (it is not behind the proxy in development).
 */
export async function signInAction(
  _previous: SignInState,
  form: FormData,
): Promise<SignInState> {
  const login = String(form.get("login") ?? "").trim();
  const password = String(form.get("password") ?? "");
  if (login === "" || password === "") return { code: "invalid_request" };

  // Where the guard was taking them; from the browser, so
  // `destinationAfterLogin` decides whether it is a same-origin path.
  const next = form.get("next");

  const jar = await cookies();

  const outcome = await signIn(
    { login, password },
    {
      fetchImpl: fetch,
      origin: apiOrigin(),
      // Forward the browser's address, or the API's per-address throttle sees
      // every student as this server.
      headers: await callerHeaders(),
      // The device token, forwarded for the same reason.
      deviceToken: jar.get(DEVICE_COOKIE)?.value,
      setCookie: (cookie) =>
        jar.set(cookie.name, cookie.value, {
          path: cookie.path ?? "/",
          maxAge: cookie.maxAge,
          httpOnly: true,
          sameSite: "lax",
          // From the deployment, not NODE_ENV: a production build served over
          // plain http must not set Secure, or the browser drops the cookie.
          secure: cookieSecure(),
        }),
    },
  );

  if (!outcome.ok) return { code: outcome.code };

  // The session exists either way; if permissions cannot be read, land on the
  // participant's screen, which every account can open.
  const identity = await fetchIdentity().catch(() => null);

  // redirect() throws, so it stays outside any try/catch.
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
