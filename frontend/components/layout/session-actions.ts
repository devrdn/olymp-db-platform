"use server";

import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import { serverRequest } from "@/lib/api/server";
import { SESSION_COOKIE } from "@/lib/auth/session";
import { signOut } from "@/lib/auth/sign-out";

/**
 * Signing out.
 *
 * A Server Action, so the control is an ordinary form: it works with
 * JavaScript switched off, and Next checks the request's Origin against its
 * Host before the action runs, which is what keeps a cross-site page from
 * signing our visitors out for fun.
 *
 * The decisions live in `signOut`; this is the wiring that gives it the
 * request and the cookie jar.
 */
export async function signOutAction() {
  const jar = await cookies();

  await signOut({
    endSession: () => serverRequest("/auth/logout", { method: "POST" }),
    clearCookie: () => jar.delete(SESSION_COOKIE),
  });

  // redirect() signals by throwing, so it stays outside anything that catches.
  redirect("/login");
}
