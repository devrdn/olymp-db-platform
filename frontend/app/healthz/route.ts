/**
 * Liveness for the web container's healthcheck (deploy/docker-compose.yml). A
 * route handler renders no layout, so it reads no cookie and calls nothing;
 * probing a page would hit the API for settings on every check. Public
 * (lib/auth/guard.ts, PUBLIC_EXACT) because the probe has no session, and
 * harmless since it only says "ok". Not under /api/, which the reverse proxy
 * (deploy/Caddyfile) sends to the API.
 */

// Answered per request, never prerendered into a static "ok".
export const dynamic = "force-dynamic";

export function GET(): Response {
  return new Response("ok", {
    status: 200,
    headers: { "content-type": "text/plain; charset=utf-8", "cache-control": "no-store" },
  });
}
