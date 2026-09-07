import { FocusShell } from "@/components/layout/focus-shell";
import { NotFoundView } from "@/components/product/not-found-view";
import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.screens.notFound.title };
}

/**
 * An address matching no route group at all.
 *
 * The one not-found that carries a shell, because there is no group layout
 * above it to supply one. Every group has its own `not-found.tsx` rendering
 * `NotFoundView` bare — see that component for why a second shell here was a
 * second header on screen.
 */
export default async function NotFound() {
  const [brand, dict, locale, theme] = await Promise.all([
    branding(),
    activeDictionary(),
    activeLocale(),
    activeTheme(),
  ]);

  return (
    <FocusShell
      name={brand.name}
      logo={brand.images.logo ? imageHref("logo", brand.images.logo) : undefined}
      locale={locale}
      theme={theme}
      dict={dict}
    >
      <NotFoundView />
    </FocusShell>
  );
}
