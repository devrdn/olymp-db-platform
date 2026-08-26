import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Theme } from "@/lib/theme/config";

import { AppBar } from "./app-bar";

/**
 * The frame the constructor wears.
 *
 * `section` names where the visitor is, in the bar, next to the mark. It is a
 * plain string rather than a nav because the constructor has one screen so
 * far; the day it has the story editor, the question list and the participant
 * import, the links go here and every screen gets them at once.
 */
export function AdminShell({
  locale,
  theme,
  dict,
  section,
  children,
}: {
  locale: Locale;
  theme: Theme;
  dict: Dictionary;
  section?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-h-[100dvh] flex-col">
      <AppBar locale={locale} theme={theme} dict={dict} home="/contests">
        {section ? <span className="truncate text-control text-ink-2">{section}</span> : null}
      </AppBar>
      <main className="flex flex-1 flex-col">{children}</main>
    </div>
  );
}
