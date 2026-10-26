import type { Metadata } from "next";

import { branding } from "@/lib/api/branding";
import { imageHref } from "@/lib/api/settings";
import { AppDictionaryProvider } from "@/lib/i18n/client";
import { selectApp } from "@/lib/i18n/scopes";
import { activeDictionary, activeLocale } from "@/lib/i18n/server";
import { themeAttribute } from "@/lib/theme/config";
import { activeTheme } from "@/lib/theme/server";

import "./globals.css";

// Served from our own origin, so a build never fetches from Google. Each family
// declares its Latin, Latin Extended and Cyrillic parts separately, and a browser
// downloads only the parts a page's text uses (Romanian needs Latin Extended).
// Variable files: the weights docs/design/SPEC.md §4 uses come from one file.
import "@fontsource-variable/onest/wght.css";
import "@fontsource-variable/jetbrains-mono/wght.css";
// Narrative register only: the crime story, nowhere else.
import "@fontsource-variable/literata/wght.css";

/**
 * The product mark, drawn by scripts/icons.mjs into public/. The ICO declares
 * its size because Chrome prefers an icon marked `any` over the SVG.
 */
const MARK_TAB = [
  { url: "/favicon.ico", sizes: "32x32" },
  { url: "/icon.svg", type: "image/svg+xml" },
];
const MARK_HOME = { url: "/apple-icon.png", sizes: "180x180", type: "image/png" };

/**
 * Title and icons come from the installation's settings, so a rename reaches
 * the tab and bookmarks; both fall back to the product's own if the read fails.
 *
 * The icons are always named here and nothing under app/ uses an icon file
 * convention: Next adds app/favicon.ico beside whatever the metadata names, so
 * an uploaded favicon would compete with it.
 */
export async function generateMetadata(): Promise<Metadata> {
  const brand = await branding();
  const name = brand.name.trim() || "DB Contest";
  const { favicon, icon } = brand.images;

  return {
    title: { default: name, template: `%s · ${name}` },
    description:
      "SQL detective contests for universities: a crime story, an isolated game database and a timer.",
    icons: {
      icon: favicon ? [{ url: imageHref("favicon", favicon) }] : MARK_TAB,
      // Not the favicon scaled up: it is drawn for 16 pixels, and iOS takes
      // only PNG or JPEG here.
      apple: icon ? [{ url: imageHref("icon", icon) }] : [MARK_HOME],
    },
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
      className="antialiased"
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
