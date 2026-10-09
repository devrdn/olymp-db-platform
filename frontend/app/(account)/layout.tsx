import { FocusShell } from "@/components/layout/focus-shell";
import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

/**
 * The frame for account screens: signed in, but with the rest of the API
 * closed. `FocusShell`, not `ProductShell`: with a one-time password the API
 * answers `password_change_required` everywhere else, so links would all
 * bounce. Its own group because these screens need a session and the guard must
 * tell them from `(public)`.
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
      name={brand.name}
      logo={brand.images.logo ? imageHref("logo", brand.images.logo) : undefined} locale={locale} theme={theme} dict={dict} signedIn>
      {children}
    </FocusShell>
  );
}
