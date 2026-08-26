import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Theme } from "@/lib/theme/config";

import { AppBar } from "./app-bar";

/**
 * What a visitor without a session sees: the bar, and the screen. No
 * navigation, because there is nowhere to go until
 * they have signed in, and an empty nav is worse than none.
 */
export function PublicShell({
  locale,
  theme,
  dict,
  children,
}: {
  locale: Locale;
  theme: Theme;
  dict: Dictionary;
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-h-[100dvh] flex-col">
      <AppBar locale={locale} theme={theme} dict={dict} />
      <main className="flex flex-1 flex-col">{children}</main>
    </div>
  );
}
