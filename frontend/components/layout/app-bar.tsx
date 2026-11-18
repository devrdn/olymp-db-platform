import Link from "next/link";

import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Theme } from "@/lib/theme/config";

import { cn } from "@/lib/utils";

import { AccountLink, type Account } from "./account-link";
import { BAR_TRACKS } from "./bar-tracks";
import type { Destination } from "./destinations";
import { LanguageSwitcher } from "./language-switcher";
import { Logo } from "./logo";
import { MenuButton } from "./menu-button";
import { SectionNav } from "./section-nav";
import { SignOutButton } from "./sign-out-button";
import { ThemeToggle } from "./theme-toggle";

const LOGO_IMAGE = "h-5 w-auto max-w-32 shrink-0 object-contain";

/**
 * Up to this many destinations fit in a row from `wide`; more fold until
 * `wider`. Four were measured not to fit beside the account at 1024px in
 * English (354px of 337) or Russian (up to 434px).
 */
const ROW_FROM_WIDE = 3;

/**
 * The classes of each fold point, written out whole so Tailwind finds them.
 * The row hides below the point and the menu from it, so exactly one shows.
 */
const FOLD = {
  wide: { row: "max-wide:hidden", menu: "wide:hidden", grow: "max-wide:grow" },
  wider: { row: "max-wider:hidden", menu: "wider:hidden", grow: "max-wider:grow" },
} as const;

/**
 * The bar every screen wears, on the bands' three-track grid so the mark and
 * the switcher align with the column below. With destinations, below `wide`
 * the row and the controls fold into the menu, and a crowded one stays folded
 * until `wider` (SPEC, "The bar below 1024 px"). Which bar shows is CSS, so
 * the server-rendered one is right before hydration.
 */
export function AppBar({
  locale,
  theme,
  dict,
  home,
  name,
  logo,
  logoDark,
  account,
  signedIn,
  destinations,
  children,
}: {
  locale: Locale;
  theme: Theme;
  dict: Dictionary;
  /** Where the mark links, if anywhere; each shell names its own home. */
  home?: string;
  name?: string;
  /** The installation's uploaded logo, if any. */
  logo?: string;
  /** The logo for the dark theme; the bar shows it instead of `logo` there. */
  logoDark?: string;
  /** When present, the bar links to the profile, where signing out lives. */
  account?: Account;
  /**
   * A session that cannot open a profile (the forced password change,
   * refused nearly every endpoint), so sign-out goes in the bar.
   */
  signedIn?: boolean;
  /** Where the account may go; when there are any, they replace `children`. */
  destinations?: Destination[];
  /** Beside the mark when there are no destinations, such as a section's name. */
  children?: React.ReactNode;
}) {
  const items = destinations?.length ? destinations : undefined;
  const fold = items ? FOLD[items.length > ROW_FROM_WIDE ? "wider" : "wide"] : undefined;

  const mark = (
    <>
      {/* Not `next/image`: the address is content-hashed and cached forever by
         the API, so an optimiser would only add a second copy and origin. */}
      {logo ? (
        // eslint-disable-next-line @next/next/no-img-element
        <img
          src={logo}
          alt=""
          aria-hidden
          // A lazy image under `display: none` has no box and is never fetched.
          loading={logoDark ? "lazy" : undefined}
          className={`${LOGO_IMAGE}${logoDark ? " light-only" : ""}`}
        />
      ) : (
        <Logo className={`h-8 w-auto shrink-0${logoDark ? " light-only" : ""}`} />
      )}
      {logoDark ? (
        // eslint-disable-next-line @next/next/no-img-element
        <img src={logoDark} alt="" aria-hidden loading="lazy" className={`${LOGO_IMAGE} dark-only`} />
      ) : null}
      {/* The installation's name, falling back to the product's. */}
      <span className="narrow:truncate max-narrow:line-clamp-2 max-narrow:leading-tight max-narrow:wrap-break-word">{name?.trim() || dict.chrome.product}</span>
    </>
  );
  const markClass = "flex min-w-0 items-center gap-2.5 text-control font-semibold text-ink";

  return (
    <header className={cn("sticky top-0 z-20 grid border-b border-line bg-bg print:hidden", BAR_TRACKS)}>
      <div />
      {/* A folded bar holds only the mark and the burger, so it never wraps. */}
      <div
        className={cn(
          "flex h-12 min-w-0 items-center gap-3.5 px-10 max-narrow:h-auto max-narrow:min-h-12 max-narrow:gap-x-3 max-narrow:gap-y-1 max-narrow:px-4.5",
          !fold && "max-narrow:flex-wrap",
        )}
      >
        {/* At phone width a long name takes two lines beside the controls. A row
           that wraps moves an item down before it shrinks it, so the mark claims
           no width and takes what the controls leave. Wider, the name keeps one
           line and is what gives way. */}
        <div className={cn("flex min-w-0 items-center max-narrow:min-h-12 max-narrow:grow max-narrow:basis-0", fold?.grow)}>
          {home ? (
            <Link href={home} className={markClass}>
              {mark}
            </Link>
          ) : (
            <span className={markClass}>{mark}</span>
          )}
        </div>

        {items || children ? (
          <>
            <span
              aria-hidden
              data-slot="app-bar-divider"
              className={cn("h-4.5 w-px shrink-0 bg-line-2 max-narrow:hidden", fold?.row)}
            />
            {/* Destinations keep their full width, which their fold point
               guarantees, and the name gives way instead. At phone width a
               section's name takes a row of its own below the mark and controls. */}
            <div
              data-slot="app-bar-nav"
              className={cn(
                "flex items-center gap-3.5 max-narrow:order-last max-narrow:basis-full",
                fold ? ["grow shrink-0", fold.row] : "min-w-0 flex-1",
              )}
            >
              {items ? <SectionNav items={items} /> : children}
            </div>
          </>
        ) : (
          <div className="flex-1 max-narrow:hidden" />
        )}

        <div
          data-slot="app-bar-controls"
          className={cn("flex shrink-0 items-center gap-1.5 max-narrow:ml-auto", fold?.row)}
        >
          <ThemeToggle current={theme} labels={dict.chrome.theme} />
          <LanguageSwitcher current={locale} label={dict.chrome.language} />
          {account ? <AccountLink account={account} /> : null}
          {!account && signedIn ? <SignOutButton label={dict.chrome.signOut} /> : null}
        </div>

        {items && fold ? (
          <MenuButton labels={dict.chrome.menu} className={cn("-mr-2.5", fold.menu)}>
            <SectionNav items={items} layout="menu" />
            <hr className="my-2 border-line" />
            {account ? <AccountLink account={account} layout="menu" /> : null}
            {!account && signedIn ? (
              <SignOutButton label={dict.chrome.signOut} withLabel className="min-h-12" />
            ) : null}
            <div className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2 pb-1">
              <ThemeToggle current={theme} labels={dict.chrome.theme} withLabel />
              <LanguageSwitcher current={locale} label={dict.chrome.language} roomy />
            </div>
          </MenuButton>
        ) : null}
      </div>
      <div />
    </header>
  );
}
