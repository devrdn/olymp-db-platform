import type { Metadata } from "next";
import { JetBrains_Mono, Literata, Onest } from "next/font/google";

import { activeLocale } from "@/lib/i18n/server";
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

export const metadata: Metadata = {
  title: { default: "DB Contest", template: "%s · DB Contest" },
  description:
    "SQL detective contests for universities: a crime story, an isolated game database and a timer.",
};

/**
 * `lang` is the language actually being rendered, read from the one place that
 * holds it. `data-theme` is read the same way, from a cookie, which is what
 * lets the first byte of HTML already carry the right theme — the alternative
 * is the blocking inline script whose only job is to repaint a page that was
 * painted wrong. Every route is dynamic anyway, because each reads the
 * session, so nothing is lost by resolving both per request.
 */
export default async function RootLayout({ children }: { children: React.ReactNode }) {
  const [locale, theme] = await Promise.all([activeLocale(), activeTheme()]);

  return (
    <html
      lang={locale}
      data-theme={themeAttribute(theme)}
      className={`${onest.variable} ${jetbrainsMono.variable} ${literata.variable} antialiased`}
    >
      <body className="font-sans text-body">{children}</body>
    </html>
  );
}
