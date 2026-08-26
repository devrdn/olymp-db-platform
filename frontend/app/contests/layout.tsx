import { AdminShell } from "@/components/layout/admin-shell";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

/**
 * The frame the whole section shares, and the reason it is a layout rather
 * than three copies in three files: `loading.tsx` and `error.tsx` render
 * inside it, so the bar does not disappear while the rows are loading and does
 * not disappear when they fail. A shell that only exists on the happy path is
 * a shell that flashes.

 */
export default async function ContestsLayout({ children }: { children: React.ReactNode }) {
  const [dict, locale, theme] = await Promise.all([
    activeDictionary(),
    activeLocale(),
    activeTheme(),
  ]);

  return (
    <AdminShell locale={locale} theme={theme} dict={dict} section={dict.contests.heading}>
      {children}
    </AdminShell>
  );
}
