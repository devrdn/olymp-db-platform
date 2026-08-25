import type { Metadata } from "next";
import { JetBrains_Mono, Literata, Onest } from "next/font/google";
import { locale as rootLocale } from "next/root-params";
import { notFound } from "next/navigation";

import { LOCALES, type Locale } from "@/lib/i18n/config";

import "../globals.css";

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

/** Every supported language is a real route, so each one can be crawled. */
export function generateStaticParams() {
  return LOCALES.map((locale) => ({ locale }));
}

/**
 * The root layout lives under the locale segment, which is what makes `locale`
 * a root parameter readable anywhere on the server. The `lang` attribute is
 * therefore the language actually being rendered, not a constant.
 */
export default async function LocaleLayout({ children }: { children: React.ReactNode }) {
  const segment = await rootLocale();
  if (!LOCALES.includes(segment as Locale)) notFound();

  return (
    <html
      lang={segment}
      className={`${onest.variable} ${jetbrainsMono.variable} ${literata.variable} h-full antialiased`}
    >
      <body className="flex min-h-full flex-col font-sans">{children}</body>
    </html>
  );
}
