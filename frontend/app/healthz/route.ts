/**
 * Liveness: whether this server is up and answering, and nothing else.
 *
 * The web container's healthcheck asks it (deploy/docker-compose.yml). It used
 * to fetch /login, and rendering any page asks the API for the site's settings
 * (lib/api/branding.ts, from the root layout's metadata): every installation,
 * idle or not, sent the API four requests a minute — on a real install almost
 * all of its access log — and web turned unhealthy whenever the API did,
 * which restarting web cannot fix.
 *
 * So this is a route handler, which renders no layout, and it reads no cookie
 * and calls nothing. It is public (lib/auth/guard.ts, PUBLIC_EXACT) because
 * the probe has no session, and harmless to be public because "ok" is all it
 * says. It is not under /api/, which the reverse proxy sends to the API.
 */

// Answered by the running server on every request, never prerendered at build
// time into a file that says "ok" whether or not anything is running.
export const dynamic = "force-dynamic";

export function GET(): Response {
  return new Response("ok", {
    status: 200,
    headers: { "content-type": "text/plain; charset=utf-8", "cache-control": "no-store" },
  });
}
