import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Theme } from "@/lib/theme/config";

import { AppBar } from "./app-bar";

/**
 * The bar, and the screen. No navigation at all.
 *
 * Not "the shell for signed-out visitors", which is what it was called while
 * sign-in was the only screen using it. Three screens do now, and one of them
 * — the forced password change — belongs to an account that is signed in. What
 * they have in common is not the absence of a session but the absence of
 * anywhere to go: each is a dead end until something is done, and offering
 * links out of it would be offering doors the API has already locked.
 *
 * An empty nav is worse than none, so there is none.
 */
export function FocusShell({
  locale,
  theme,
  dict,
  signedIn,
  children,
}: {
  locale: Locale;
  theme: Theme;
  dict: Dictionary;
  /**
   * The forced password change is the one screen here that belongs to a
   * signed-in account, and the one that most needs a way out: the API closes
   * every other endpoint until the password is replaced, so without it the
   * only escape from somebody else's handover password is clearing a cookie
   * by hand. `/auth/logout` is exempt from that gate on the server for exactly
   * this reason.
   */
  signedIn?: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-h-[100dvh] flex-col">
      <AppBar locale={locale} theme={theme} dict={dict} signedIn={signedIn} />
      <main className="flex flex-1 flex-col">{children}</main>
    </div>
  );
}
