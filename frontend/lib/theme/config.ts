export const THEMES = ["system", "light", "dark"] as const;
export const DEFAULT_THEME = "system";
export type Theme = (typeof THEMES)[number];

/**
 * Where a chosen theme is remembered.
 *
 * A cookie rather than localStorage, for the same reason the language is one:
 * the server has to know before it renders. The alternative is the blocking
 * inline script every dark-mode implementation ships, which exists only to
 * repaint the page before the user sees the wrong one. Reading a cookie the
 * server already receives removes the flash instead of hiding it, and every
 * route here is dynamic anyway because each reads the session.
 */
export const THEME_COOKIE = "dbcontest_theme";

export function readTheme(stored: string | undefined | null): Theme {
  return THEMES.includes(stored as Theme) ? (stored as Theme) : DEFAULT_THEME;
}

/**
 * The attribute stamped on `<html>`.
 *
 * `system` stamps nothing: the absence of the attribute is what lets the
 * `prefers-color-scheme` block in tokens.css apply. Writing `data-theme="system"`
 * would need a third branch in CSS that says exactly the same thing.
 */
export function themeAttribute(theme: Theme): "light" | "dark" | undefined {
  return theme === "system" ? undefined : theme;
}

/** The order the single cycling control walks. */
export function nextTheme(theme: Theme): Theme {
  return THEMES[(THEMES.indexOf(theme) + 1) % THEMES.length];
}
