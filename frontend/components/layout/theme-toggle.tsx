import { Monitor, Moon, Sun } from "lucide-react";

import { nextTheme, type Theme } from "@/lib/theme/config";

import { chooseTheme } from "./theme-actions";

const ICONS: Record<Theme, typeof Monitor> = {
  system: Monitor,
  light: Sun,
  dark: Moon,
};

/**
 * One control cycling three states. It submits the next theme to the server,
 * which reads the cookie before rendering, so there is no flash. The accessible
 * name says where the press leads.
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
