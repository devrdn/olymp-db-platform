import Link from "next/link";

import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Theme } from "@/lib/theme/config";

import { AccountLink, type Account } from "./account-link";
import { LanguageSwitcher } from "./language-switcher";
import { Logo } from "./logo";
import { SignOutButton } from "./sign-out-button";
import { ThemeToggle } from "./theme-toggle";

const LOGO_IMAGE = "h-5 w-auto max-w-32 shrink-0 object-contain";

/**
 * The bar every screen wears, on the bands' three-track grid so the mark and
 * the switcher align with the column below.
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
  children?: React.ReactNode;
}) {
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
      <span className="max-narrow:line-clamp-2 max-narrow:leading-tight max-narrow:wrap-break-word">{name?.trim() || dict.chrome.product}</span>
    </>
  );
  const markClass = "flex min-w-0 items-center gap-2.5 text-control font-semibold text-ink";

  return (
    <header className="sticky top-0 z-20 grid grid-cols-[minmax(var(--gutter-min),1fr)_minmax(0,var(--container-column))_minmax(var(--gutter-min),1fr)] border-b border-line bg-bg max-narrow:grid-cols-[0_minmax(0,1fr)_0] print:hidden">
      <div />
      <div className="flex h-12 min-w-0 items-center gap-3.5 px-10 max-narrow:h-auto max-narrow:min-h-12 max-narrow:flex-wrap max-narrow:gap-x-3 max-narrow:gap-y-1 max-narrow:px-4.5">
        {/* At phone width a long name takes two lines beside the controls. A row
           that wraps moves an item down before it shrinks it, so the mark claims
           no width and takes what the controls leave. */}
        <div className="flex shrink-0 items-center max-narrow:min-h-12 max-narrow:min-w-0 max-narrow:shrink max-narrow:grow max-narrow:basis-0">
          {home ? (
            <Link href={home} className={markClass}>
              {mark}
            </Link>
          ) : (
            <span className={markClass}>{mark}</span>
          )}
        </div>

        {children ? (
          <>
            <span aria-hidden className="h-4.5 w-px shrink-0 bg-line-2 max-narrow:hidden" />
            {/* At phone width the destinations take a row of their own below the mark
               and controls, scrolling sideways rather than overlapping them. The
               padding keeps the scroller from clipping the focus ring (2px plus a
               2px offset) at its edges. */}
            <div
              data-slot="app-bar-nav"
              className="flex min-w-0 flex-1 items-center gap-3.5 max-narrow:order-last max-narrow:basis-full max-narrow:-mx-1 max-narrow:overflow-x-auto max-narrow:p-1"
            >
              {children}
            </div>
          </>
        ) : (
          <div className="flex-1 max-narrow:hidden" />
        )}

        <div className="flex shrink-0 items-center gap-1.5 max-narrow:ml-auto">
          <ThemeToggle current={theme} labels={dict.chrome.theme} />
          <LanguageSwitcher current={locale} label={dict.chrome.language} />
          {account ? <AccountLink account={account} /> : null}
          {!account && signedIn ? <SignOutButton label={dict.chrome.signOut} /> : null}
        </div>
      </div>
      <div />
    </header>
  );
}
