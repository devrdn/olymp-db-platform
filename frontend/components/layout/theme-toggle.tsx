"use client";

import { useSyncExternalStore } from "react";

import { Monitor, Moon, Sun } from "lucide-react";

import { nextTheme, readTheme, THEME_COOKIE, themeAttribute, type Theme } from "@/lib/theme/config";

const ICONS: Record<Theme, typeof Monitor> = {
  system: Monitor,
  light: Sun,
  dark: Moon,
};

const ONE_YEAR_SECONDS = 60 * 60 * 24 * 365;

// Every toggle on the page (the bar's and the footer's) reads the same
// attribute, so a press on one turns the other's icon too.
const listeners = new Set<() => void>();

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function pageTheme(): Theme {
  return readTheme(document.documentElement.getAttribute("data-theme") ?? "system");
}

/**
 * Applies a theme in the browser alone: the attribute the stylesheet keys on,
 * and the cookie the root layout reads on the next request, so a reload or a
 * new tab renders it from the first byte. Transitions are held off for the swap
 * so every colour changes in one frame rather than each control easing on its
 * own.
 */
function applyTheme(theme: Theme) {
  const root = document.documentElement;
  root.setAttribute("data-theme-swap", "");
  const attribute = themeAttribute(theme);
  if (attribute) root.setAttribute("data-theme", attribute);
  else root.removeAttribute("data-theme");
  document.cookie = `${THEME_COOKIE}=${theme}; path=/; max-age=${ONE_YEAR_SECONDS}; samesite=lax`;
  // Reading a style computes the new colours while transitions are still off.
  void getComputedStyle(document.body).color;
  setTimeout(() => root.removeAttribute("data-theme-swap"), 0);
  for (const listener of listeners) listener();
}

/**
 * One control cycling three states. The server stamps the first theme from the
 * cookie, so there is no flash; a press changes it here without a request. The
 * accessible name says where the press leads.
 */
export function ThemeToggle({
  current,
  labels,
}: {
  /** The theme the server rendered with, until the browser can read the page. */
  current: Theme;
  labels: Record<Theme, string>;
}) {
  const theme = useSyncExternalStore(subscribe, pageTheme, () => current);
  const next = nextTheme(theme);
  const Icon = ICONS[theme];

  return (
    <button
      type="button"
      onClick={() => applyTheme(next)}
      title={labels[next]}
      aria-label={labels[next]}
      className="grid size-7 place-items-center rounded-full text-ink-3 transition-colors duration-(--t-input) ease-standard hover:bg-sunk hover:text-ink"
    >
      <Icon className="size-4" strokeWidth={1.75} aria-hidden />
    </button>
  );
}
