import { Band } from "@/components/layout/band";
import { PublicShell } from "@/components/layout/public-shell";
import { StateView } from "@/components/product/state-view";
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
 * falling back to the framework's English default. It is `error-terminal` from
 * section 7 and not `error-recoverable`: a wrong address does not become right
 * on a second attempt, so it offers a way out instead of a retry.
 */
export default async function NotFound() {
  const [dict, locale, theme] = await Promise.all([
    activeDictionary(),
    activeLocale(),
    activeTheme(),
  ]);
  const t = dict.screens.notFound;

  return (
    <PublicShell locale={locale} theme={theme} dict={dict}>
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
    </PublicShell>
  );
}
