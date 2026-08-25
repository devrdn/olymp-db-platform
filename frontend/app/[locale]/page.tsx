import { redirect } from "next/navigation";

import { activeLocale } from "@/lib/i18n/server";

/** Nothing lives at the language root yet; the register is the entry point. */
export default async function LocaleHome() {
  redirect(`/${await activeLocale()}/contests`);
}
