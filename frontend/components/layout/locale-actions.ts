"use server";

import { cookies } from "next/headers";
import { revalidatePath } from "next/cache";

import { LOCALE_COOKIE, LOCALES, type Locale } from "@/lib/i18n/config";

/**
 * Records the chosen language.
 *
 * A Server Action rather than a cookie written from the browser: the language
 * is read on the server before a page renders, and doing the write here means
 * the switcher also works with JavaScript switched off. The visitor stays
 * exactly where they were, because the language is not part of the address.
 */
export async function chooseLocale(form: FormData) {
  const requested = String(form.get("locale") ?? "");
  if (!LOCALES.includes(requested as Locale)) return;

  const jar = await cookies();
  jar.set(LOCALE_COOKIE, requested, {
    path: "/",
    maxAge: 60 * 60 * 24 * 365,
    sameSite: "lax",
    // Nothing secret here, and the server is the only reader that matters.
    httpOnly: false,
  });

  revalidatePath("/", "layout");
}
