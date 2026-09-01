import { redirect } from "next/navigation";

import { ProductShell } from "@/components/layout/product-shell";
import { homeFor } from "@/lib/auth/destination";
import { fetchIdentity } from "@/lib/auth/session";
import { branding } from "@/lib/api/branding";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

/**
 * The frame for a screen both audiences share.
 *
 * `(admin)` and `(participant)` each know where their own home is. This group
 * is for what neither owns — the profile — so the mark is pointed from the
 * permissions the API reports, the same rule sign-in follows when it decides
 * where to land.
 *
 * No navigation: the profile is a place you arrive at from the bar and leave
 * by the mark, and offering the register to a participant who cannot open it
 * would be offering a door that answers 403.
 */
export default async function SessionLayout({ children }: { children: React.ReactNode }) {
  const [brand, dict, locale, theme, identity] = await Promise.all([
    branding(),
    activeDictionary(),
    activeLocale(),
    activeTheme(),
    fetchIdentity(),
  ]);

  // The bar's own account door led here, so a request arriving without a
  // usable session has one: it lapsed in between. Back to the form.
  if (!identity) redirect("/login?next=%2Fprofile");

  return (
    <ProductShell
      name={brand.name}
      locale={locale}
      theme={theme}
      dict={dict}
      home={homeFor(identity.permissions)}
      account={{ fullName: identity.fullName, login: identity.login }}
    >
      {children}
    </ProductShell>
  );
}
