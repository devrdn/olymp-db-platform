import { cookies } from "next/headers";

import { callerHeaders } from "@/lib/api/caller";
import { apiOrigin } from "@/lib/api/config";
import type { Relay } from "@/lib/api/signup";

import { browserCookieOptions } from "./cookie-policy";

/**
 * A relay bound to the current request: the browser's cookies named in
 * `forward` go to the API, and every cookie the API answers with goes back
 * to the browser.
 */
export async function browserRelay(forward: readonly string[]): Promise<Relay> {
  const jar = await cookies();
  const cookie = forward
    .flatMap((name) => {
      const value = jar.get(name)?.value;
      return value ? [`${name}=${value}`] : [];
    })
    .join("; ");

  return {
    origin: apiOrigin(),
    headers: await callerHeaders(),
    cookie: cookie || undefined,
    setCookie: (parsed) => jar.set(parsed.name, parsed.value, browserCookieOptions(parsed)),
  };
}
