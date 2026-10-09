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
 * Not-found for addresses outside every route group; the only one with a shell,
 * since no group layout supplies one.
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
