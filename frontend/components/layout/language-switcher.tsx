import { LOCALE_NAMES, LOCALES, type Locale } from "@/lib/i18n/config";

import { chooseLocale } from "./locale-actions";

/**
 * A form of submit buttons, so it works without JavaScript; the language is not
 * in the address, so the visitor stays where they are.
 */
export function LanguageSwitcher({ current, label }: { current: Locale; label: string }) {
  return (
    <form
      action={chooseLocale}
      aria-label={label}
      className="inline-flex overflow-hidden rounded-full border border-line-2"
    >
      {LOCALES.map((locale) => {
        const active = locale === current;
        return (
          <button
            key={locale}
            type="submit"
            name="locale"
            value={locale}
            lang={locale}
            aria-current={active ? "true" : undefined}
            title={LOCALE_NAMES[locale]}
            className={[
              "px-2 py-1 font-mono text-label uppercase",
              "transition-colors duration-(--t-input) ease-standard",
              active ? "bg-cta text-cta-fg" : "text-ink-3 hover:text-ink",
            ].join(" ")}
          >
            {locale}
          </button>
        );
      })}
    </form>
  );
}
