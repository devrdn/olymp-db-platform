import { AdminShell } from "@/components/layout/admin-shell";
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
 * It is a layout rather than something each page wraps itself in because
 * `loading.tsx` and `error.tsx` render inside it: the bar stays put while rows
 * load and stays put when they fail. A shell that only exists on the happy
 * path is a shell that flashes.

 */
export default async function AdminLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  const [dict, locale, theme] = await Promise.all([
    activeDictionary(),
    activeLocale(),
    activeTheme(),
  ]);

  return (
    <AdminShell
      locale={locale}
      theme={theme}
      dict={dict}
      section={dict.contests.heading}
    >
      {children}
    </AdminShell>
  );
}
