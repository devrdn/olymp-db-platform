import { cookies } from "next/headers";

import { branding } from "@/lib/api/branding";
import { readPage } from "@/lib/api/page-read";
import { publicContestsSchema, publicStatsSchema } from "@/lib/api/showcase";
import { sessionCookie } from "@/lib/auth/session";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

import { ConsolePreview } from "./home/console-preview";
import { Hero } from "./home/hero";
import { HowItWorks } from "./home/how-it-works";
import { Numbers } from "./home/numbers";
import { OrganiserLine } from "./home/organiser-line";
import { RecentContests } from "./home/recent-contests";
import { SiteFooter } from "./home/site-footer";

/**
 * The front page, the same for everyone; signing in only changes the hero's
 * main action. All reads happen here and sections get plain values, so each
 * renders in tests without an API. Each section is a `Band`, on the same grid
 * as the working screens.
 */
export default async function HomePage() {
  const [brand, dict, locale, theme, jar] = await Promise.all([
    branding(),
    activeDictionary(),
    activeLocale(),
    activeTheme(),
    cookies(),
  ]);

  /**
   * The cookie's presence, as the route guard checks. Asking the API would cost
   * a round trip for strangers; a stale cookie only offers a link that ends at
   * sign-in anyway.
   */
  const signedIn = jar.has(sessionCookie().name);

  // Resolved once for both hero and footer, so they agree.
  const name = brand.name.trim() || dict.chrome.product;

  /**
   * Best-effort: neither read should take the page down, and a section shows
   * nothing for a failed one. The failure is logged, since this is the only
   * place "API down" differs from "new installation"; the public read budget
   * is not, since any visitor can spend it at will. Contests are read in the
   * visitor's language.
   */
  const policy = { here: "/", absent: ["public_too_often"], bestEffort: true } as const;
  const [stats, contests] = await Promise.all([
    readPage("/public/stats", (payload) => publicStatsSchema.parse(payload), policy),
    readPage(`/public/contests?lang=${locale}`, (payload) => publicContestsSchema.parse(payload).items, policy),
  ]);

  return (
    <>
      <Hero name={name} signedIn={signedIn} dict={dict} />

      <Numbers stats={stats} dict={dict} />

      <HowItWorks dict={dict} />

      {/* Shown after the points that describe it. */}
      <ConsolePreview dict={dict} />

      <RecentContests
        signedIn={signedIn}
        archive={brand.archive}
        contests={contests}
        dict={dict}
        locale={locale}
      />

      <OrganiserLine dict={dict} />

      <SiteFooter
        name={name}
        contact={brand.contact}
        locale={locale}
        theme={theme}
        dict={dict}
      />
    </>
  );
}
