import { ProductShell } from "@/components/layout/product-shell";
import { SectionNav } from "@/components/layout/section-nav";
import { fetchIdentity } from "@/lib/auth/session";
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
  const [dict, locale, theme, identity] = await Promise.all([
    activeDictionary(),
    activeLocale(),
    activeTheme(),
    fetchIdentity(),
  ]);

  return (
    <ProductShell
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
      {children}
    </ProductShell>
  );
}
