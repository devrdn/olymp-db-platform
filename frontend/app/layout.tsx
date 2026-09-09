import type { Metadata } from "next";
import { JetBrains_Mono, Literata, Onest } from "next/font/google";

import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { AppDictionary } from "@/lib/i18n/client";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { themeAttribute } from "@/lib/theme/config";
import { activeTheme } from "@/lib/theme/server";

import "./globals.css";

// Three languages, so three subsets. The scaffold shipped `["latin"]`, which
// silently drops Cyrillic and the Romanian comma-below letters and renders
// them from a fallback face instead. The lists are written out at each call
// because next/font reads them at build time and cannot follow a reference.

// Weights are pinned rather than left to the variable default so the bundle
// carries what section 4 uses and nothing else. There is no 700: the system
// has no bold, and shipping the file would be an invitation to reach for it.
const onest = Onest({
  variable: "--font-onest",
  subsets: ["latin", "latin-ext", "cyrillic"],
  weight: ["400", "500", "600"],
  display: "swap",
});

const jetbrainsMono = JetBrains_Mono({
  variable: "--font-jetbrains-mono",
  subsets: ["latin", "latin-ext", "cyrillic"],
  weight: ["400", "500"],
  display: "swap",
});

// Narrative register only: the crime story, nowhere else.
const literata = Literata({
  variable: "--font-literata",
  subsets: ["latin", "latin-ext", "cyrillic"],
  weight: ["400"],
  display: "swap",
});

/**
 * The title and the tab icon come from the installation.
 *
 * `generateMetadata` rather than a constant, because both are rows in a table
 * now: a university that renamed itself would otherwise be renamed everywhere
 * on screen and still called "DB Contest" in the browser's tab, its history
 * and every bookmark somebody made.
 *
 * Both fall back to the product's own. A settings row that cannot be read is
 * not a reason to serve a page with no title.
 */
export async function generateMetadata(): Promise<Metadata> {
  const brand = await branding();
  const name = brand.name.trim() || "DB Contest";

  const icons: Metadata["icons"] = {};
  if (brand.images.favicon) icons.icon = imageHref("favicon", brand.images.favicon);
  // The large square one: a home screen, a bookmark tile. Falls back to the
  // favicon rather than to nothing, since a small icon scaled up beats none.
  const large = brand.images.icon ?? brand.images.favicon;
  if (large) icons.apple = imageHref(brand.images.icon ? "icon" : "favicon", large);

  return {
    title: { default: name, template: `%s · ${name}` },
    description:
      "SQL detective contests for universities: a crime story, an isolated game database and a timer.",
    ...(icons.icon || icons.apple ? { icons } : {}),
  };
}

/**
 * `lang` is the language actually being rendered, read from the one place that
 * holds it. `data-theme` is read the same way, from a cookie, which is what
 * lets the first byte of HTML already carry the right theme — the alternative
 * is the blocking inline script whose only job is to repaint a page that was
 * painted wrong. Every route is dynamic anyway, because each reads the
 * session, so nothing is lost by resolving both per request.
 */
export default async function RootLayout({ children }: { children: React.ReactNode }) {
  const [locale, dict, theme] = await Promise.all([
    activeLocale(),
    activeDictionary(),
    activeTheme(),
  ]);

  return (
    <html
      lang={locale}
      data-theme={themeAttribute(theme)}
      className={`${onest.variable} ${jetbrainsMono.variable} ${literata.variable} antialiased`}
    >
      <body className="font-sans text-body">
        {/* At the root rather than per section: `error.tsx` is a client
            boundary that can never await a dictionary, and any route can end
            on one. What crosses is this scope's own sections and no others
            (finding 5) — the root boundary reads one, and a group whose
            boundaries read more adds its own scope in its own layout. */}
        <AppDictionary.Provider dict={AppDictionary.select(dict)} locale={locale}>
          {children}
        </AppDictionary.Provider>
      </body>
    </html>
  );
}
