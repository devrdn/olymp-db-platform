import { redirect } from "next/navigation";

import { ProductShell } from "@/components/layout/product-shell";
import { homeFor } from "@/lib/auth/destination";
import { fetchIdentity } from "@/lib/auth/session";
import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { SessionDictionaryProvider } from "@/lib/i18n/client";
import { selectSession } from "@/lib/i18n/scopes";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

/**
 * The frame for the profile, which both audiences share. The mark's target
 * comes from the API's permissions, as sign-in's landing does. No navigation:
 * the register would be a 403 for a participant.
 */
export default async function SessionLayout({ children }: { children: React.ReactNode }) {
  const [brand, dict, locale, theme, identity] = await Promise.all([
    branding(),
    activeDictionary(),
    activeLocale(),
    activeTheme(),
    fetchIdentity(),
  ]);

  // The session lapsed since the bar linked here; back to sign-in.
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
      {/* This group's dictionary sections (`profile`, plus retry wording from
         `participant`), selected on the server so only what the subtree reads
         crosses the wire. */}
      <SessionDictionaryProvider dict={selectSession(dict)} locale={locale}>
        {children}
      </SessionDictionaryProvider>
    </ProductShell>
  );
}
