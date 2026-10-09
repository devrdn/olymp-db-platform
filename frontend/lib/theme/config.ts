export const THEMES = ["system", "light", "dark"] as const;
export const DEFAULT_THEME = "system";
export type Theme = (typeof THEMES)[number];

/**
 * A cookie, not localStorage: the server renders the right theme first, with
 * no blocking inline script to repaint the page.
 */
export const THEME_COOKIE = "dbcontest_theme";

export function readTheme(stored: string | undefined | null): Theme {
  return THEMES.includes(stored as Theme) ? (stored as Theme) : DEFAULT_THEME;
}

/**
 * The `data-theme` on `<html>`. `system` stamps nothing, which lets the
 * `prefers-color-scheme` block in tokens.css apply.
 */
export function themeAttribute(theme: Theme): "light" | "dark" | undefined {
  return theme === "system" ? undefined : theme;
}

/** The order the cycling control walks. */
export function nextTheme(theme: Theme): Theme {
  return THEMES[(THEMES.indexOf(theme) + 1) % THEMES.length];
}
