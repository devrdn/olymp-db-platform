import { cookies } from "next/headers";

import { Band } from "@/components/layout/band";
import { OrnamentBand } from "@/components/product/ornament";
import { branding } from "@/lib/api/branding";
import { SESSION_COOKIE } from "@/lib/auth/session";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { activeTheme } from "@/lib/theme/server";

import { Hero } from "./home/hero";
import { HowItWorks } from "./home/how-it-works";
import { OrganiserLine } from "./home/organiser-line";
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
 * The page is composed out of `Band`s, so its sections sit on the same three
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

  return (
    <>
      <Hero name={name} signedIn={signedIn} dict={dict} />

      {/* The woven band that separates the hero from everything below it. It
          gets a band of its own rather than padding inside another, so it
          spans the content column exactly as the sections above and below do.

          Taller than the component's own default, and that is what makes it
          embroidery rather than texture: `slice` scales the tile by the
          band's height, so at the default 24px each rhombus is twelve pixels
          across and the whole thing reads as a hairline ripple — looked at in
          a browser, the motif simply cannot be made out. At 36 the rhombi and
          the rivers between them are legible at arm's length, and the band is
          still a rule rather than a panel. The footer's stays thin on
          purpose: one statement, one echo. */}
      <Band className="py-7 max-narrow:py-6">
        <OrnamentBand className="h-9" />
      </Band>

      <HowItWorks dict={dict} />

      {/* The contests this installation has run, and the four numbers above
          them, are a later step. The hero's second action points at
          `#contests`, which is the anchor that section will carry. */}

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
