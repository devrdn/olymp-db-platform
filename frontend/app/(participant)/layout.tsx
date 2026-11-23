import { ProductShell } from "@/components/layout/product-shell";
import { participantDestinations } from "@/components/layout/destinations";
import { readIdentity } from "@/lib/api/page-read";
import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { ParticipantDictionaryProvider } from "@/lib/i18n/client";
import { selectParticipant } from "@/lib/i18n/scopes";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

/**
 * The frame of a participant's screens: the same shell as the constructor's,
 * but its mark leads home to `/my`. A student sent to `/contests` would meet
 * the author's register scoped to contests they manage, which is empty for
 * them. Its destinations, `/my` and `/open` (and the archive when on), as navigation: "when does mine
 * start" and "what can I join" are different questions.
 */
export default async function ParticipantLayout({ children }: { children: React.ReactNode }) {
  const [brand, dict, locale, theme, identity] = await Promise.all([
    branding(),
    activeDictionary(),
    activeLocale(),
    activeTheme(),
    // Tolerated only here: this layout decorates and never redirects, so an
    // unreachable server costs only the account chip. Screens that decide
    // where somebody may go let the failure through, since "unknown" must not
    // read as "signed out".
    readIdentity(),
  ]);

  return (
    <ProductShell
      name={brand.name}
      logo={brand.images.logo ? imageHref("logo", brand.images.logo) : undefined}
      logoDark={brand.images["logo-dark"] ? imageHref("logo-dark", brand.images["logo-dark"]) : undefined}
      locale={locale}
      theme={theme}
      dict={dict}
      home="/my"
      destinations={participantDestinations(dict, brand.archive)}
      account={identity ? { fullName: identity.fullName, login: identity.login } : undefined}
    >
      {/* The participant dictionary scope (`/my`, `/open` read `participant`),
          narrowed on the server so only what this subtree reads crosses the
          wire. */}
      <ParticipantDictionaryProvider dict={selectParticipant(dict)} locale={locale}>
        {children}
      </ParticipantDictionaryProvider>
    </ProductShell>
  );
}
