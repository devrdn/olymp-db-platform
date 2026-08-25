import { DEFAULT_LOCALE, LOCALES, type Locale } from "./config";
import { matchLocale } from "./locale";

/**
 * Where a request without a language prefix should go.
 *
 * Every page lives under a locale segment, which keeps the language in the URL
 * where it can be shared, bookmarked and read by a crawler. A request that
 * already names one is left alone; anything else is negotiated from the
 * browser's own preferences and redirected once.
 */

/** Parses an Accept-Language header into tags ordered by their q-weight. */
export function parseAcceptLanguage(header: string | null): string[] {
  if (!header) return [];

  return header
    .split(",")
    .map((part) => {
      const [tag, ...params] = part.trim().split(";");
      const q = params
        .map((p) => p.trim())
        .find((p) => p.startsWith("q="))
        ?.slice(2);
      return { tag: tag.trim(), weight: q === undefined ? 1 : Number(q) };
    })
    .filter((entry) => entry.tag !== "" && !Number.isNaN(entry.weight))
    .sort((a, b) => b.weight - a.weight)
    .map((entry) => entry.tag);
}

function hasLocalePrefix(pathname: string): boolean {
  const first = pathname.split("/")[1];
  return LOCALES.includes(first as Locale);
}

export function localeRedirect(pathname: string, acceptLanguage: string | null): string | null {
  if (hasLocalePrefix(pathname)) return null;

  const locale = matchLocale(parseAcceptLanguage(acceptLanguage), [...LOCALES], DEFAULT_LOCALE);
  const rest = pathname === "/" ? "" : pathname;

  return `/${locale}${rest}`;
}
