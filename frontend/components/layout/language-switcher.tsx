import { LOCALE_NAMES, LOCALES, type Locale } from "@/lib/i18n/config";

import { chooseLocale } from "./locale-actions";

/**
 * Changing language leaves the visitor exactly where they were: the language
 * is not part of the address, so there is nothing to navigate.
 *
 * A form, so it needs no JavaScript. Each language is a submit button.
 *
 * The width is fixed by the codes themselves — two letters, in every language
 * there will ever be — which is the one place in this interface where a
 * container may be sized to its content (spec section 8).
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
