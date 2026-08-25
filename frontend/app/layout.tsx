import type { Metadata } from "next";
import { JetBrains_Mono, Literata, Onest } from "next/font/google";

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
  title: {
    default: "DB Contest",
    template: "%s · DB Contest",
  },
  description: "Платформа университетских SQL-олимпиад формата «Детектив».",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html
      lang="ru"
      className={`${onest.variable} ${jetbrainsMono.variable} ${literata.variable} h-full antialiased`}
    >
      <body className="flex min-h-full flex-col font-sans">{children}</body>
    </html>
  );
}
