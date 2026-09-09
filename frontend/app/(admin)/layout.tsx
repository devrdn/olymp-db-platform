import { SectionNav } from "@/components/layout/section-nav";
import { ProductShell } from "@/components/layout/product-shell";
import { fetchIdentity } from "@/lib/auth/session";
import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { AdminDictionary } from "@/lib/i18n/client";
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
  const [brand, dict, locale, theme, identity] = await Promise.all([
    branding(),
    activeDictionary(),
    activeLocale(),
    activeTheme(),
    // Tolerated here, and only here: this layout decorates and never
    // redirects, so a server that cannot be asked costs the audit link and the account door and nothing
    // else. The screens that decide where somebody may go let the failure
    // through, because there "unknown" must not be answered as "signed out".
    fetchIdentity().catch(() => null),
  ]);

  // Offered from the permissions the API reports, which is the same thing its
  // middleware decides on — so a role given the audit permission as data gains
  // the link without a change here, and an account without it is never shown a
  // door it cannot open.
  const may = (permission: string) => identity?.permissions.includes(permission) ?? false;
  const destinations = [
    { href: "/contests", label: dict.contests.heading },
    ...(may("users.manage") ? [{ href: "/users", label: dict.accounts.heading }] : []),
    ...(may("audit.view") ? [{ href: "/audit", label: dict.audit.heading }] : []),
    ...(may("settings.manage") ? [{ href: "/settings", label: dict.settings.heading }] : []),
  ];

  return (
    <ProductShell
      name={brand.name}
      logo={brand.images.logo ? imageHref("logo", brand.images.logo) : undefined}
      locale={locale}
      theme={theme}
      dict={dict}
      home="/contests"
      nav={<SectionNav items={destinations} />}
      account={identity ? { fullName: identity.fullName, login: identity.login } : undefined}
    >
      {/* This group's own dictionary scope, on top of the root's: the four constructor boundaries each name their own screen,
          and none of that is a section the root scope carries. The sections
          are chosen here, on the server, so what crosses the wire is what
          this subtree can actually read (finding 5). */}
      <AdminDictionary.Provider dict={AdminDictionary.select(dict)} locale={locale}>
        {children}
      </AdminDictionary.Provider>
    </ProductShell>
  );
}
