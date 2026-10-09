import type { Metadata } from "next";
import { JetBrains_Mono, Literata, Onest } from "next/font/google";

import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { AppDictionaryProvider } from "@/lib/i18n/client";
import { selectApp } from "@/lib/i18n/scopes";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { themeAttribute } from "@/lib/theme/config";
import { activeTheme } from "@/lib/theme/server";

import "./globals.css";

// Latin alone drops Cyrillic and Romanian comma-below letters to a fallback
// face. The lists are literal because next/font reads them at build time.

// Only the weights docs/design/SPEC.md §4 uses; no 700, since the system has
// no bold.
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
 * Title and icons come from the installation's settings, so a rename reaches
 * the tab and bookmarks; both fall back to the product's own if the read fails.
 */
export async function generateMetadata(): Promise<Metadata> {
  const brand = await branding();
  const name = brand.name.trim() || "DB Contest";

  const icons: Metadata["icons"] = {};
  if (brand.images.favicon) icons.icon = imageHref("favicon", brand.images.favicon);
  // The large icon falls back to the favicon: scaled up beats none.
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
 * `lang` and `data-theme` are resolved per request (every route is dynamic
 * anyway), so the first byte carries the right theme without a blocking repaint
 * script.
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
        {/* At the root because `error.tsx` is a client boundary that cannot
           await a dictionary. Only this scope's sections cross the wire; groups
           add their own scopes. */}
        <AppDictionaryProvider dict={selectApp(dict)} locale={locale}>
          {children}
        </AppDictionaryProvider>
      </body>
    </html>
  );
}
