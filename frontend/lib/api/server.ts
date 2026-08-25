import { cookies } from "next/headers";

import { request, type RequestOptions } from "./client";

/**
 * Server-side access to the Core API.
 *
 * The session is an httpOnly cookie the browser will not hand to JavaScript,
 * and a Server Component's fetch carries no cookies of its own. So the cookie
 * is read here and forwarded explicitly. In the browser the same request needs
 * nothing, because it is same-origin through the reverse proxy.
 */

const SESSION_COOKIE = "dbcontest_session";

/** Where the API lives from the server's point of view (never the browser's). */
function apiOrigin(): string {
  return process.env.API_ORIGIN ?? "http://localhost:8080";
}

export async function serverRequest(path: string, options: RequestOptions = {}) {
  // Next 16: cookies() is async and synchronous access has been removed.
  const jar = await cookies();
  const session = jar.get(SESSION_COOKIE)?.value;

  return request(path, {
    ...options,
    origin: apiOrigin(),
    headers: {
      ...options.headers,
      ...(session ? { cookie: `${SESSION_COOKIE}=${session}` } : {}),
    },
  });
}
