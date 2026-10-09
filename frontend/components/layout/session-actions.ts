"use server";

import { cookies } from "next/headers";
import { redirect } from "next/navigation";

import { serverRequest } from "@/lib/api/server";
import { SESSION_COOKIE } from "@/lib/auth/session";
import { signOut } from "@/lib/auth/sign-out";

/**
 * Sign-out as a Server Action: works without JavaScript, and Next checks Origin
 * against Host before it runs. The decisions live in `signOut`.
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
