import Link from "next/link";

import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Theme } from "@/lib/theme/config";

import { cn } from "@/lib/utils";

import { AccountLink, type Account } from "./account-link";
import type { Destination } from "./destinations";
import { LanguageSwitcher } from "./language-switcher";
import { Logo } from "./logo";
import { MenuButton } from "./menu-button";
import { SectionNav } from "./section-nav";
import { SignOutButton } from "./sign-out-button";
import { ThemeToggle } from "./theme-toggle";

const LOGO_IMAGE = "h-5 w-auto max-w-32 shrink-0 object-contain";

/**
 * The bar every screen wears, on the bands' three-track grid so the mark and
 * the switcher align with the column below. With destinations, below `wide`
 * the row and the controls fold into the menu (SPEC, "The bar below 1024 px");
 * which bar shows is CSS, so the server-rendered one is right before hydration.
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
    <header className="sticky top-0 z-20 grid grid-cols-[minmax(var(--gutter-min),1fr)_minmax(0,var(--container-column))_minmax(var(--gutter-min),1fr)] border-b border-line bg-bg max-narrow:grid-cols-[0_minmax(0,1fr)_0] print:hidden">
      <div />
      {/* Positioned, so the menu's panel hangs under this column and not the whole bar. */}
      <div className="relative flex h-12 min-w-0 items-center gap-3.5 px-10 max-narrow:h-auto max-narrow:min-h-12 max-narrow:flex-wrap max-narrow:gap-x-3 max-narrow:gap-y-1 max-narrow:px-4.5">
        {/* At phone width a long name takes two lines beside the controls. A row
           that wraps moves an item down before it shrinks it, so the mark claims
           no width and takes what the controls leave. Wider, the name keeps one
           line and gives way first, up to a third of the row so the destinations
           keep the rest. */}
        <div
          className={cn(
            "flex min-w-0 items-center wide:max-w-1/3 max-narrow:min-h-12 max-narrow:grow max-narrow:basis-0",
            items && "max-wide:grow",
          )}
        >
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
            <span aria-hidden className={cn("h-4.5 w-px shrink-0 bg-line-2 max-narrow:hidden", items && "max-wide:hidden")} />
            {/* At phone width a section's name takes a row of its own below the mark
               and controls; destinations are in the menu by then. */}
            <div
              data-slot="app-bar-nav"
              className={cn(
                "flex min-w-0 flex-1 items-center gap-3.5 max-narrow:order-last max-narrow:basis-full",
                items && "max-wide:hidden",
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
          className={cn("flex shrink-0 items-center gap-1.5 max-narrow:ml-auto", items && "max-wide:hidden")}
        >
          <ThemeToggle current={theme} labels={dict.chrome.theme} />
          <LanguageSwitcher current={locale} label={dict.chrome.language} />
          {account ? <AccountLink account={account} /> : null}
          {!account && signedIn ? <SignOutButton label={dict.chrome.signOut} /> : null}
        </div>

        {items ? (
          <MenuButton labels={dict.chrome.menu} className="-mr-2.5 wide:hidden">
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
