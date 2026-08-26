import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Theme } from "@/lib/theme/config";

import { AppBar } from "./app-bar";

/**
 * The frame every screen behind a session wears.
 *
 * One shell, not one per audience. It was `AdminShell` while the constructor
 * was the only thing behind a session; the participant's screens arrived and
 * wanted the identical bar with a different home, which is a prop, not a
 * component. Two shells differing by one string is how the language switcher
 * gets fixed in one of them and not the other.
 *
 * `home` is where the mark leads, and it differs by audience for a real
 * reason: an author's home is the register, a participant's is their own list,
 * and sending either to the other's screen means an immediate refusal from the
 * API. `section` names where the visitor is, beside the mark.
 *
 * It is a layout rather than something each page wraps itself in because
 * `loading.tsx` and `error.tsx` render inside it: the bar stays put while rows
 * load and stays put when they fail. A shell that exists only on the happy
 * path is a shell that flashes.
 */
export function ProductShell({
  locale,
  theme,
  dict,
  home,
  section,
  children,
}: {
  locale: Locale;
  theme: Theme;
  dict: Dictionary;
  home: string;
  section?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-h-[100dvh] flex-col">
      <AppBar locale={locale} theme={theme} dict={dict} home={home}>
        {section ? <span className="truncate text-control text-ink-2">{section}</span> : null}
      </AppBar>
      <main className="flex flex-1 flex-col">{children}</main>
    </div>
  );
}
