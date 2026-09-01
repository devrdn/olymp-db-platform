import { FocusShell } from "@/components/layout/focus-shell";
import { branding } from "@/lib/api/branding";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

/**
 * The frame worn by a screen that belongs to the account rather than to the
 * product: signed in, but with the rest of the API still closed.
 *
 * It is `FocusShell` and not `AdminShell` because the account holding a
 * one-time password cannot reach the constructor — the API answers
 * `password_change_required` on every other endpoint. A bar with links to
 * screens that would all bounce is a worse lie than a bar with none.
 *
 * A group of its own rather than a folder under `(public)`: these screens do
 * require a session, and the guard has to be able to tell the two apart.
 */
export default async function AccountLayout({ children }: { children: React.ReactNode }) {
  const [brand, dict, locale, theme] = await Promise.all([
    branding(),
    activeDictionary(),
    activeLocale(),
    activeTheme(),
  ]);

  return (
    <FocusShell
      name={brand.name} locale={locale} theme={theme} dict={dict} signedIn>
      {children}
    </FocusShell>
  );
}
