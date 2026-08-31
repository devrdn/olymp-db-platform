import { AdminNav } from "@/components/layout/admin-nav";
import { ProductShell } from "@/components/layout/product-shell";
import { fetchIdentity } from "@/lib/auth/session";
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
  const [dict, locale, theme, identity] = await Promise.all([
    activeDictionary(),
    activeLocale(),
    activeTheme(),
    fetchIdentity(),
  ]);

  // Offered from the permissions the API reports, which is the same thing its
  // middleware decides on — so a role given the audit permission as data gains
  // the link without a change here, and an account without it is never shown a
  // door it cannot open.
  const may = (permission: string) => identity?.permissions.includes(permission) ?? false;
  const destinations = [
    { href: "/contests", label: dict.contests.heading },
    ...(may("audit.view") ? [{ href: "/audit", label: dict.audit.heading }] : []),
  ];

  return (
    <ProductShell
      locale={locale}
      theme={theme}
      dict={dict}
      home="/contests"
      nav={<AdminNav items={destinations} />}
    >
      {children}
    </ProductShell>
  );
}
