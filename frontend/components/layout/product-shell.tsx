import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Theme } from "@/lib/theme/config";

import type { Account } from "./account-link";
import { AppBar } from "./app-bar";
import type { Destination } from "./destinations";

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
  destinations,
  account,
  name,
  logo,
  logoDark,
  children,
}: {
  locale: Locale;
  theme: Theme;
  dict: Dictionary;
  home: string;
  section?: string;
  /** Replace the section label rather than join it, which would say the same thing twice. */
  destinations?: Destination[];
  /** Absent when the layout could not reach `/auth/me`; the bar then says less. */
  account?: Account;
  name?: string;
  /** The installation's uploaded logo, if any. */
  logo?: string;
  /** The logo for the dark theme, if any. */
  logoDark?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-h-[100dvh] flex-col">
      <AppBar
        locale={locale}
        theme={theme}
        dict={dict}
        home={home}
        account={account}
        name={name}
        logo={logo}
        logoDark={logoDark}
        destinations={destinations}
      >
        {section ? <span className="truncate text-control text-ink-2">{section}</span> : null}
      </AppBar>
      <main className="flex flex-1 flex-col">{children}</main>
    </div>
  );
}
