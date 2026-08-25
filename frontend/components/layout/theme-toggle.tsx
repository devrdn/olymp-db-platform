import { Monitor, Moon, Sun } from "lucide-react";

import { nextTheme, type Theme } from "@/lib/theme/config";

import { chooseTheme } from "./theme-actions";

const ICONS: Record<Theme, typeof Monitor> = {
  system: Monitor,
  light: Sun,
  dark: Moon,
};

/**
 * One control for three states, because three controls for three states is
 * three times the chrome for a setting most people touch once.
 *
 * It submits the *next* theme rather than toggling in the browser: the choice
 * is a cookie the server reads before rendering, so the page comes back in the
 * new theme already painted, with no flash and no hydration step. The
 * accessible name says where the press leads, not where it is.
 */
export function ThemeToggle({
  current,
  labels,
}: {
  current: Theme;
  labels: Record<Theme, string>;
}) {
  const next = nextTheme(current);
  const Icon = ICONS[current];

  return (
    <form action={chooseTheme}>
      <button
        type="submit"
        name="theme"
        value={next}
        title={labels[next]}
        aria-label={labels[next]}
        className="grid size-7 place-items-center rounded-full text-ink-3 transition-colors duration-(--t-input) ease-standard hover:bg-sunk hover:text-ink"
      >
        <Icon className="size-4" strokeWidth={1.75} aria-hidden />
      </button>
    </form>
  );
}
