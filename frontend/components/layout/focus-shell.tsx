import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Theme } from "@/lib/theme/config";

import { AppBar } from "./app-bar";

/**
 * The bar and the screen, with no section navigation: for the public screens,
 * the not-found page and the forced password change.
 */
export function FocusShell({
  locale,
  theme,
  dict,
  signedIn,
  name,
  logo,
  children,
}: {
  locale: Locale;
  theme: Theme;
  dict: Dictionary;
  /**
   * For the forced password change: the API closes every other endpoint until
   * the password is replaced, so sign-out must be offered here (`/auth/logout`
   * is exempt on the server).
   */
  signedIn?: boolean;
  name?: string;
  /** The installation's uploaded logo, if any. */
  logo?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="flex min-h-[100dvh] flex-col">
      <AppBar locale={locale} theme={theme} dict={dict} signedIn={signedIn} name={name} logo={logo} />
      <main className="flex flex-1 flex-col">{children}</main>
    </div>
  );
}
