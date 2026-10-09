import { cookies } from "next/headers";

import { branding } from "@/lib/api/branding";
import { serverRequest } from "@/lib/api/server";
import { publicContestsSchema, publicStatsSchema } from "@/lib/api/showcase";
import { SESSION_COOKIE } from "@/lib/auth/session";
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
  const signedIn = jar.has(SESSION_COOKIE);

  // Resolved once for both hero and footer, so they agree.
  const name = brand.name.trim() || dict.home.defaultName;

  /**
   * Settled, not joined: neither read should take the page down, and each
   * section handles nothing. Contests are read in the visitor's language.
   */
  const [stats, contests] = await Promise.allSettled([
    serverRequest("/public/stats").then((payload) => publicStatsSchema.parse(payload)),
    serverRequest(`/public/contests?lang=${locale}`).then(
      (payload) => publicContestsSchema.parse(payload).items,
    ),
  ]);

  return (
    <>
      <Hero name={name} signedIn={signedIn} dict={dict} />

      <Numbers stats={settled(stats, "/public/stats")} dict={dict} />

      <HowItWorks dict={dict} />

      {/* Shown after the points that describe it. */}
      <ConsolePreview dict={dict} />

      <RecentContests
        signedIn={signedIn}
        contests={settled(contests, "/public/contests")}
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

/**
 * A settled read's value, or `null`. The reason is logged, since the page
 * cannot show it and this is the only place "API down" differs from "new
 * installation".
 */
function settled<T>(result: PromiseSettledResult<T>, path: string): T | null {
  if (result.status === "fulfilled") return result.value;
  console.error("reading %s for the front page failed", path, result.reason);
  return null;
}
