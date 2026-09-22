import { Band } from "@/components/layout/band";
import { LanguageSwitcher } from "@/components/layout/language-switcher";
import { ThemeToggle } from "@/components/layout/theme-toggle";
import { OrnamentBand } from "@/components/product/ornament";
import type { Locale } from "@/lib/i18n/config";
import type { Dictionary } from "@/lib/i18n/dictionary";
import type { Theme } from "@/lib/theme/config";

/**
 * Whether a contact is an address somebody can write to.
 *
 * The setting is one free-text row, and an installation may well put a room
 * number or a telephone in it. Only something shaped like an email becomes a
 * `mailto:` link; anything else is shown as the text it is, because a link
 * that opens a mail client on "Block C, room 214" is a broken promise, and
 * refusing to show it at all would be hiding the one way to reach anybody.
 */
function mailAddress(contact: string): string {
  const trimmed = contact.trim();
  return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed) ? trimmed : "";
}

/**
 * The foot of the front page, and the first footer this product has.
 *
 * The working screens have none on purpose: they are a workspace, and
 * everything they offer is in the bar. This page is read rather than worked
 * in, and it is the one page a stranger arrives on, so it ends by saying whose
 * installation this is and how to reach them.
 *
 * The language and theme controls are the bar's own components rather than
 * copies. Both are forms submitting to Server Actions, so they work here
 * exactly as they do above — with no JavaScript, and with the page coming back
 * already painted in the new theme. A visitor who has scrolled the whole page
 * should not have to scroll back up to change either.
 *
 * Everything is handed in: the page above reads the settings, the cookies and
 * the dictionary once, which keeps this renderable without a running API.
 */
export function SiteFooter({
  name,
  contact,
  locale,
  theme,
  dict,
}: {
  /** What this installation calls itself, already resolved to something. */
  name: string;
  /** How to reach whoever runs it, straight from the settings row. */
  contact: string;
  locale: Locale;
  theme: Theme;
  dict: Dictionary;
}) {
  const email = mailAddress(contact);

  return (
    <footer>
      <Band className="gap-8 py-10 max-narrow:py-8">
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

        {/* The page closes on the woven edge it opened under: walnut rather
            than indigo, because the sub-palette's rule is one colour per band,
            and half the height, because this ends the page rather than
            dividing it.

            The height is taken by cropping the band, not by giving the band a
            shorter box. `slice` scales the whole tile to cover its box, so a
            12px box would draw the pattern at half size — hairlines included,
            and a half-pixel stroke is lighter than every rule on the page. The
            band keeps its own 24px and a window shows the middle of it, which
            is the row of rhombi with the rivers cut off above and below: a
            selvedge, which is what a woven border looks like where the cloth
            was cut. */}
        <div className="h-3 overflow-hidden">
          <OrnamentBand className="-mt-1.5 text-ornament-walnut" />
        </div>
      </Band>
    </footer>
  );
}
