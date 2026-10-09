import { FocusShell } from "@/components/layout/focus-shell";
import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

/** The frame for visitors without a session. `(public)` is a route group, absent from the URL. */
export default async function PublicLayout({ children }: { children: React.ReactNode }) {
  const [brand, dict, locale, theme] = await Promise.all([
    branding(),
    activeDictionary(),
    activeLocale(),
    activeTheme(),
  ]);

  return (
    <FocusShell
      name={brand.name}
      logo={brand.images.logo ? imageHref("logo", brand.images.logo) : undefined} locale={locale} theme={theme} dict={dict}>
      {children}
    </FocusShell>
  );
}
