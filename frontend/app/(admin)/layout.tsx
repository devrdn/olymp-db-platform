import { ProductShell } from "@/components/layout/product-shell";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

/**
 * The frame every administrative screen wears.
 *
 * A route group rather than a folder in the URL: `(admin)` collects the screens
 * behind a session without appearing in any address, so the next one — the
 * story editor, the question list, the participant import — drops in beside
 * `contests` and arrives already framed.
 *
 * The shell itself is shared with the participant's group. What differs is
 * where the mark leads, which is a prop.
 */
export default async function AdminLayout({ children }: { children: React.ReactNode }) {
  const [dict, locale, theme] = await Promise.all([
    activeDictionary(),
    activeLocale(),
    activeTheme(),
  ]);

  return (
    <ProductShell
      locale={locale}
      theme={theme}
      dict={dict}
      home="/contests"
      section={dict.contests.heading}
    >
      {children}
    </ProductShell>
  );
}
