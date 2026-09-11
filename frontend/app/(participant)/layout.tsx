import { ProductShell } from "@/components/layout/product-shell";
import { SectionNav } from "@/components/layout/section-nav";
import { fetchIdentity } from "@/lib/auth/session";
import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { ParticipantDictionaryProvider } from "@/lib/i18n/client";
import { selectParticipant } from "@/lib/i18n/scopes";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

/**
 * The frame a participant's screens wear.
 *
 * The same shell as the constructor's, pointed at a different home. A student
 * sent to `/contests` would meet the register scoped to contests they manage,
 * which for them is empty — an accurate answer to a question they never asked.
 *
 * A group of its own rather than a folder under `(admin)` because the audience
 * is the thing that differs, and it is the audience that decides where the
 * mark leads. When the case screen and the SQL console arrive in step 5 they
 * belong here, already framed.
 *
 * Two destinations, so a section label gives way to navigation. They answer
 * different questions — "when does mine start", asked under a timer on the
 * day, and "what can I join", browsed once a term — which is why they are two
 * screens and not one list.
 */
export default async function ParticipantLayout({ children }: { children: React.ReactNode }) {
  const [brand, dict, locale, theme, identity] = await Promise.all([
    branding(),
    activeDictionary(),
    activeLocale(),
    activeTheme(),
    // Tolerated here, and only here: this layout decorates and never
    // redirects, so a server that cannot be asked costs the account door and nothing
    // else. The screens that decide where somebody may go let the failure
    // through, because there "unknown" must not be answered as "signed out".
    fetchIdentity().catch(() => null),
  ]);

  return (
    <ProductShell
      name={brand.name}
      logo={brand.images.logo ? imageHref("logo", brand.images.logo) : undefined}
      locale={locale}
      theme={theme}
      dict={dict}
      home="/my"
      nav={
        <SectionNav
          items={[
            { href: "/my", label: dict.participant.mine.heading },
            { href: "/open", label: dict.participant.open.heading },
          ]}
        />
      }
      account={identity ? { fullName: identity.fullName, login: identity.login } : undefined}
    >
      {/* This group's own dictionary scope, on top of the root's: the participant's own boundaries (`/my`, `/open`) read from `participant`,
          and none of that is a section the root scope carries. The sections
          are chosen here, on the server, so what crosses the wire is what
          this subtree can actually read (finding 5). */}
      <ParticipantDictionaryProvider dict={selectParticipant(dict)} locale={locale}>
        {children}
      </ParticipantDictionaryProvider>
    </ProductShell>
  );
}
