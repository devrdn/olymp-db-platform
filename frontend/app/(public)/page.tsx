import { cookies } from "next/headers";

import { branding } from "@/lib/api/branding";
import { serverRequest } from "@/lib/api/server";
import { publicContestsSchema, publicStatsSchema } from "@/lib/api/showcase";
import { SESSION_COOKIE } from "@/lib/auth/session";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

import { Hero } from "./home/hero";
import { HowItWorks } from "./home/how-it-works";
import { Numbers } from "./home/numbers";
import { OrganiserLine } from "./home/organiser-line";
import { RecentContests } from "./home/recent-contests";
import { SiteFooter } from "./home/site-footer";

/**
 * The front page: what this installation is, and what is happening on it.
 *
 * Until now `/` sent every visitor to the sign-in form, so somebody handed a
 * link to an olympiad met a login box before learning where they had arrived.
 * This is the screen that answers "what is this", and it is the same screen
 * for everybody — signing in changes one thing, which is where the hero's main
 * action leads (see `Hero`).
 *
 * Every read happens here, once, and the sections are handed plain values.
 * That is what keeps each of them renderable in a test without a running API,
 * and it keeps the question "what does this page ask for" answerable in one
 * place rather than in five.
 *
 * Every section is a `Band` of its own, so they sit on the same three
 * grid tracks and the same hairlines as every working screen: a showcase that
 * invented its own container would read as an advertisement for the product
 * rather than as part of it.
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
   * The cookie's presence, which is what the route guard decides on too.
   *
   * Asking the API who the caller is would cost a round trip on the one page
   * that strangers load, and would buy nothing: the worst a stale cookie does
   * here is offer "my contests" to somebody whose session has expired, and
   * following it lands them on the sign-in form, which is where they were
   * going anyway.
   */
  const signedIn = jar.has(SESSION_COOKIE);

  // What the installation calls itself, or the product's own name for one that
  // has not renamed anything. Resolved once and given to both the hero and the
  // footer, so the page cannot say two different things about whose it is.
  const name = brand.name.trim() || dict.home.defaultName;

  /**
   * The two public reads, settled rather than joined.
   *
   * `Promise.all` would reject the whole page on either one, which is the
   * wrong trade twice over: the numbers are the cheaper half of the screen and
   * the list is what somebody came for, and neither is worth the other. Each
   * settles on its own, and each section already knows what to do with
   * nothing — the strip disappears, the list explains itself.
   *
   * The contests read states the visitor's language, because a contest carries
   * its own translations and the server picks per contest from what it is
   * told.
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

      <HowItWorks dict={dict} />

      <Numbers stats={settled(stats, "/public/stats")} dict={dict} />

      <RecentContests
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
 * What a settled read gave, or `null` when it gave nothing.
 *
 * The reason goes to the server log, because otherwise it exists nowhere: the
 * page will have rendered successfully, and neither section can say on screen
 * what went wrong — a stranger who has asked for nothing is owed an answer,
 * not an apology. This is the only place the difference between "the API is
 * down" and "this installation is new" survives.
 */
function settled<T>(result: PromiseSettledResult<T>, path: string): T | null {
  if (result.status === "fulfilled") return result.value;
  console.error("reading %s for the front page failed", path, result.reason);
  return null;
}
