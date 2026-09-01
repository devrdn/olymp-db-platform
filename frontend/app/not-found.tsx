import { Band } from "@/components/layout/band";
import { FocusShell } from "@/components/layout/focus-shell";
import { StateView } from "@/components/product/state-view";
import { branding } from "@/lib/api/branding";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

export async function generateMetadata() {
  const dict = await activeDictionary();
  return { title: dict.screens.notFound.title };
}

/**
 * The address that leads nowhere.
 *
 * A Server Component, so it is translated like every other screen rather than
 * falling back to the framework's English default. It is a terminal error and
 * not a recoverable one: a wrong address does not become right
 * on a second attempt, so it offers a way out instead of a retry.
 */
export default async function NotFound() {
  const [brand, dict, locale, theme] = await Promise.all([
    branding(),
    activeDictionary(),
    activeLocale(),
    activeTheme(),
  ]);
  const t = dict.screens.notFound;

  return (
    <FocusShell
      name={brand.name} locale={locale} theme={theme} dict={dict}>
      <Band fill>
        <StateView
          state={{
            kind: "error-terminal",
            title: t.title,
            body: t.body,
            exit: { label: t.home, href: "/contests" },
          }}
        />
      </Band>
    </FocusShell>
  );
}
