import { redirect } from "next/navigation";

import { ProductShell } from "@/components/layout/product-shell";
import { homeFor } from "@/lib/auth/destination";
import { fetchIdentity } from "@/lib/auth/session";
import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { SessionDictionary } from "@/lib/i18n/client";
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
      logo={brand.images.logo ? imageHref("logo", brand.images.logo) : undefined}
      locale={locale}
      theme={theme}
      dict={dict}
      home={homeFor(identity.permissions)}
      account={{ fullName: identity.fullName, login: identity.login }}
    >
      {/* This group's own dictionary scope, on top of the root's: the profile's boundary reads from `profile` and borrows the retry wording from `participant`,
          and none of that is a section the root scope carries. The sections
          are chosen here, on the server, so what crosses the wire is what
          this subtree can actually read (finding 5). */}
      <SessionDictionary.Provider dict={SessionDictionary.select(dict)} locale={locale}>
        {children}
      </SessionDictionary.Provider>
    </ProductShell>
  );
}
