import Link from "next/link";

import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Theme } from "@/lib/theme/config";

import { AccountLink, type Account } from "./account-link";
import { LanguageSwitcher } from "./language-switcher";
import { Mark } from "./mark";
import { SignOutButton } from "./sign-out-button";
import { ThemeToggle } from "./theme-toggle";

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
        <img src={logo} alt="" aria-hidden className="h-5 w-auto max-w-32 shrink-0 object-contain" />
      ) : (
        <span aria-hidden className="grid size-5 shrink-0 place-items-center bg-cta text-cta-fg">
          <Mark className="size-3" />
        </span>
      )}
      {/* The installation's name, falling back to the product's. */}
      {name?.trim() || dict.chrome.product}
    </>
  );
  const markClass = "flex shrink-0 items-center gap-2 text-control font-semibold text-ink";

  return (
    <header className="sticky top-0 z-20 grid grid-cols-[minmax(var(--gutter-min),1fr)_minmax(0,var(--container-column))_minmax(var(--gutter-min),1fr)] border-b border-line bg-bg max-narrow:grid-cols-[0_minmax(0,1fr)_0] print:hidden">
      <div />
      <div className="flex h-12 min-w-0 items-center gap-3.5 px-10 max-narrow:px-4.5">
        {home ? (
          <Link href={home} className={markClass}>
            {mark}
          </Link>
        ) : (
          <span className={markClass}>{mark}</span>
        )}

        {children ? (
          <>
            <span aria-hidden className="h-4.5 w-px shrink-0 bg-line-2" />
            <div className="flex min-w-0 flex-1 items-center gap-3.5">{children}</div>
          </>
        ) : (
          <div className="flex-1" />
        )}

        <div className="flex shrink-0 items-center gap-1.5">
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
