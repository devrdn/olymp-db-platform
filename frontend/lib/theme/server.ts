import { cookies } from "next/headers";

import { readTheme, THEME_COOKIE, type Theme } from "./config";

/** The active theme, for Server Components. */
export async function activeTheme(): Promise<Theme> {
  const jar = await cookies();
  return readTheme(jar.get(THEME_COOKIE)?.value);
}
