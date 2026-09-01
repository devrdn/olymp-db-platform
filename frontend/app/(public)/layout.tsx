import { FocusShell } from "@/components/layout/focus-shell";
import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

/**
 * The frame a visitor without a session wears.
 *
 * A route group rather than a folder in the URL: `(public)` groups the screens
 * that share this shell without appearing in any address. The alternative was
 * each page wrapping itself, which is how sign-in and the constructor ended up
 * putting their shells on in two different places.
 */
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
