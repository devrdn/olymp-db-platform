import { LOCALE_NAMES, LOCALES, type Locale } from "@/lib/i18n/config";

import { chooseLocale } from "./locale-actions";

/**
 * Changing language leaves the visitor exactly where they were: the language
 * is not part of the address, so there is nothing to navigate.
 *
 * A form, so it needs no JavaScript. Each language is a submit button.
 */
export function LanguageSwitcher({ current }: { current: Locale }) {
  return (
    <form action={chooseLocale} className="flex items-center gap-px" aria-label="Language">
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
              "px-2 py-1 font-mono text-[0.625rem] tracking-[0.08em] uppercase transition-colors duration-150",
              "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent",
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
