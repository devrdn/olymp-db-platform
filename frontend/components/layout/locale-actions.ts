"use server";

import { cookies } from "next/headers";
import { revalidatePath } from "next/cache";

import { LOCALE_COOKIE, LOCALES, type Locale } from "@/lib/i18n/config";

/**
 * Records the chosen language in a cookie the server reads before rendering. A
 * Server Action, so the switcher works without JavaScript.
 */
export async function chooseLocale(form: FormData) {
  const requested = String(form.get("locale") ?? "");
  if (!LOCALES.includes(requested as Locale)) return;

  const jar = await cookies();
  jar.set(LOCALE_COOKIE, requested, {
    path: "/",
    maxAge: 60 * 60 * 24 * 365,
    sameSite: "lax",
    // Not secret; the server is the reader that matters.
    httpOnly: false,
  });

  revalidatePath("/", "layout");
}
