import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Theme } from "@/lib/theme/config";

import type { Account } from "./account-link";
import { AppBar } from "./app-bar";

/**
 * The frame for every screen behind a session. One shell for all audiences;
 * `home` differs because each audience's home is refused to the other. A
 * layout, so `loading.tsx` and `error.tsx` render inside it and the bar never
 * flashes.
 */
export function ProductShell({
  locale,
  theme,
  dict,
  home,
  section,
  nav,
  account,
  name,
  logo,
  children,
}: {
  locale: Locale;
  theme: Theme;
  dict: Dictionary;
  home: string;
  section?: string;
  /** Replaces the section label rather than joining it, which would say the same thing twice. */
  nav?: React.ReactNode;
  /** Absent when the layout could not reach `/auth/me`; the bar then says less. */
  account?: Account;
  name?: string;
  /** The installation's uploaded logo, if any. */
  logo?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-h-[100dvh] flex-col">
      <AppBar locale={locale} theme={theme} dict={dict} home={home} account={account} name={name} logo={logo}>
        {nav ?? (section ? <span className="truncate text-control text-ink-2">{section}</span> : null)}
      </AppBar>
      <main className="flex flex-1 flex-col">{children}</main>
    </div>
  );
}
