import { notFound } from "next/navigation";

import { destinationsFor } from "@/components/layout/destinations";
import { ProductShell } from "@/components/layout/product-shell";
import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { homeFor } from "@/lib/auth/destination";
import { fetchIdentity } from "@/lib/auth/session";
import { ArchiveDictionaryProvider } from "@/lib/i18n/client";
import { selectArchive } from "@/lib/i18n/scopes";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

/**
 * The frame of the contest archive, which staff and participants both read.
 * Its menu is the viewer's own, with this section among it, and the mark leads
 * to the viewer's own home: the other audience's would be refused.
 *
 * It never sends anybody to sign-in. proxy.ts already does for a visitor with
 * no cookie; a cookie whose session has died is left to the page, whose own
 * read is refused and which alone knows the address to come back to.
 */
export default async function ArchiveLayout({ children }: { children: React.ReactNode }) {
  const [brand, dict, locale, theme] = await Promise.all([
    branding(),
    activeDictionary(),
    activeLocale(),
    activeTheme(),
  ]);
  // Here, outside the pages' loading boundary: once a Suspense fallback has
  // streamed the status is already 200, so only the layout can answer a real
  // 404 while the archive is off, and it does so before asking who this is.
  // The pages check again.
  if (!brand.archive) notFound();
  // Not caught: an unreachable server must not read as "signed out".
  const identity = await fetchIdentity();

  return (
    <ProductShell
      name={brand.name}
      logo={brand.images.logo ? imageHref("logo", brand.images.logo) : undefined}
      logoDark={brand.images["logo-dark"] ? imageHref("logo-dark", brand.images["logo-dark"]) : undefined}
      locale={locale}
      theme={theme}
      dict={dict}
      home={identity ? homeFor(identity) : "/"}
      destinations={identity ? destinationsFor(identity, dict, brand.archive) : undefined}
      account={identity ? { fullName: identity.fullName, login: identity.login } : undefined}
    >
      {/* The `archive` section, for the error boundary, which cannot await the dictionary. */}
      <ArchiveDictionaryProvider dict={selectArchive(dict)} locale={locale}>
        {children}
      </ArchiveDictionaryProvider>
    </ProductShell>
  );
}
