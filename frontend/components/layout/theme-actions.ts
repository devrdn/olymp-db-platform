"use server";

import { cookies } from "next/headers";
import { revalidatePath } from "next/cache";

import { THEME_COOKIE, THEMES, type Theme } from "@/lib/theme/config";

/**
 * Records the chosen theme. A Server Action, so it works without JavaScript and
 * the next render already carries the right attribute.
 */
export async function chooseTheme(form: FormData) {
  const requested = String(form.get("theme") ?? "");
  if (!THEMES.includes(requested as Theme)) return;

  const jar = await cookies();
  jar.set(THEME_COOKIE, requested, {
    path: "/",
    maxAge: 60 * 60 * 24 * 365,
    sameSite: "lax",
    httpOnly: false,
  });

  revalidatePath("/", "layout");
}
