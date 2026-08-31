import Link from "next/link";

import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Theme } from "@/lib/theme/config";

import { LanguageSwitcher } from "./language-switcher";
import { Mark } from "./mark";
import { SignOutButton } from "./sign-out-button";
import { ThemeToggle } from "./theme-toggle";

/**
 * The bar every screen wears.
 *
 * Its contents sit on the same three-track grid the bands use, so the mark
 * lines up with the first character of the heading below it and the language
 * switcher with the right edge of the column. A bar that floats full-bleed
 * over a centred column is the small misalignment that makes a page look
 * assembled from parts, and it costs one grid to avoid.
 */
export function AppBar({
  locale,
  theme,
  dict,
  home,
  signedIn,
  children,
}: {
  locale: Locale;
  theme: Theme;
  dict: Dictionary;
  /**
   * Where the mark leads, when it leads anywhere.
   *
   * A mark that navigates is a promise that the destination exists, and `/`
   * has no page: the public landing belongs to a later step. Each shell says
   * where its own home is, and the sign-in screen says nothing, because a
   * visitor without a session has nowhere to be sent but back here.
   */
  home?: string;
  /**
   * Whether there is a session to end. A control that ends nothing invites a
   * press to find out what it does, so the sign-in screen does not carry one.
   */
  signedIn?: boolean;
  /** Screen-specific chrome: a contest title, a timer, a breadcrumb. */
  children?: React.ReactNode;
}) {
  const mark = (
    <>
      <span aria-hidden className="grid size-5 shrink-0 place-items-center bg-cta text-cta-fg">
        <Mark className="size-3" />
      </span>
      {dict.chrome.product}
    </>
  );
  const markClass = "flex shrink-0 items-center gap-2 text-control font-semibold text-ink";

  return (
    <header className="sticky top-0 z-20 grid grid-cols-[minmax(var(--gutter-min),1fr)_minmax(0,var(--container-column))_minmax(var(--gutter-min),1fr)] border-b border-line bg-bg max-narrow:grid-cols-[0_minmax(0,1fr)_0]">
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
          {signedIn ? <SignOutButton label={dict.chrome.signOut} /> : null}
        </div>
      </div>
      <div />
    </header>
  );
}
