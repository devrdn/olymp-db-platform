import { staffDestinations } from "@/components/layout/destinations";
import { ProductShell } from "@/components/layout/product-shell";
import { readIdentity } from "@/lib/api/page-read";
import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { AdminDictionaryProvider } from "@/lib/i18n/client";
import { selectAdmin } from "@/lib/i18n/scopes";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

/**
 * The frame for administrative screens. `(admin)` is a route group, absent from
 * the URL. The shell is shared with participants; only the mark's target
 * differs.
 */
export default async function AdminLayout({ children }: { children: React.ReactNode }) {
  const [brand, dict, locale, theme, identity] = await Promise.all([
    branding(),
    activeDictionary(),
    activeLocale(),
    activeTheme(),
    // Tolerated here only: this layout decorates and never redirects, so a
    // failure costs the audit link and account door. Screens that gate access
    // let the error propagate, since "unknown" must not mean "signed out".
    readIdentity(),
  ]);

  const destinations = staffDestinations(dict, identity?.permissions ?? [], brand.archive);

  return (
    <ProductShell
      name={brand.name}
      logo={brand.images.logo ? imageHref("logo", brand.images.logo) : undefined}
      logoDark={brand.images["logo-dark"] ? imageHref("logo-dark", brand.images["logo-dark"]) : undefined}
      locale={locale}
      theme={theme}
      dict={dict}
      home="/contests"
      destinations={destinations}
      account={identity ? { fullName: identity.fullName, login: identity.login } : undefined}
    >
      {/* This group's dictionary sections, selected on the server so only what
         the subtree reads crosses the wire. */}
      <AdminDictionaryProvider dict={selectAdmin(dict)} locale={locale}>
        {children}
      </AdminDictionaryProvider>
    </ProductShell>
  );
}
