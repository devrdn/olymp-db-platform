import type { Metadata } from "next";
import { JetBrains_Mono, Literata, Onest } from "next/font/google";

import { activeLocale } from "@/lib/i18n/server";

import "./globals.css";

// Three languages, so three subsets. The scaffold shipped `["latin"]`, which
// silently drops Cyrillic and the Romanian comma-below letters and renders
// them from a fallback face instead. The lists are written out at each call
// because next/font reads them at build time and cannot follow a reference.

const onest = Onest({
  variable: "--font-onest",
  subsets: ["latin", "latin-ext", "cyrillic"],
  display: "swap",
});

const jetbrainsMono = JetBrains_Mono({
  variable: "--font-jetbrains-mono",
  subsets: ["latin", "latin-ext", "cyrillic"],
  display: "swap",
});

// Narrative register only: the crime story, nowhere else.
const literata = Literata({
  variable: "--font-literata",
  subsets: ["latin", "latin-ext", "cyrillic"],
  display: "swap",
});

export const metadata: Metadata = {
  title: { default: "DB Contest", template: "%s · DB Contest" },
};

/**
 * `lang` is the language actually being rendered, read from the one place that
 * holds it. Every route is dynamic anyway, because each reads the session, so
 * nothing is lost by resolving the language per request.
 */
export default async function RootLayout({ children }: { children: React.ReactNode }) {
  const locale = await activeLocale();

  return (
    <html
      lang={locale}
      className={`${onest.variable} ${jetbrainsMono.variable} ${literata.variable} h-full antialiased`}
    >
      <body className="flex min-h-full flex-col font-sans">{children}</body>
    </html>
  );
}
