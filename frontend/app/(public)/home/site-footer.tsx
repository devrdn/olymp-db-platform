import { Band } from "@/components/layout/band";
import { LanguageSwitcher } from "@/components/layout/language-switcher";
import { ThemeToggle } from "@/components/layout/theme-toggle";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Theme } from "@/lib/theme/config";

/**
 * The contact as an email address, or empty. The setting is free text and may
 * hold a room or phone number, which is shown as plain text.
 */
function mailAddress(contact: string): string {
  const trimmed = contact.trim();
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed) ? trimmed : "";
}

/**
 * The front page footer: whose installation this is and how to reach them, plus
 * the bar's own language and theme controls (Server Action forms, so they work
 * without JavaScript). Everything is passed in, so it renders without an API.
 */
export function SiteFooter({
  name,
  contact,
  locale,
  theme,
  dict,
}: {
  /** The installation's name, already resolved. */
  name: string;
  /** The raw contact setting. */
  contact: string;
  locale: Locale;
  theme: Theme;
  dict: Dictionary;
}) {
  const email = mailAddress(contact);

  return (
    <footer>
      {/* The last band draws no rule under itself. */}
      <Band rule={false} className="gap-8 py-10 max-narrow:py-8">
        <div className="flex flex-wrap items-center justify-between gap-x-8 gap-y-5">
          <div className="flex min-w-0 flex-wrap items-baseline gap-x-4 gap-y-1">
            <span className="text-control font-semibold text-ink">{name}</span>
            {email ? (
              <a
                href={`mailto:${email}`}
                className="text-small text-ink-3 underline-offset-4 transition-colors duration-(--t-input) ease-standard hover:text-ink hover:underline"
              >
                {contact}
              </a>
            ) : contact.trim() ? (
              <span className="text-small text-ink-3">{contact}</span>
            ) : null}
          </div>

          <div className="flex shrink-0 items-center gap-1.5">
            <ThemeToggle current={theme} labels={dict.chrome.theme} />
            <LanguageSwitcher current={locale} label={dict.chrome.language} />
          </div>
        </div>

              </Band>
    </footer>
  );
}
