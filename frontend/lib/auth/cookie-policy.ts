/**
 * Whether this deployment's session cookie is marked `Secure`.
 *
 * It used to be `process.env.NODE_ENV === "production"`, and that is the wrong
 * question asked of the wrong variable. NODE_ENV describes how the code was
 * built. Whether the browser reaches this application over TLS is a property
 * of the deployment, and the two come apart the moment a production build is
 * served over plain http — which is exactly what `make front-start` does, and
 * what a staging box behind no proxy would do.
 *
 * When they came apart, the cookie was marked Secure and the browser silently
 * discarded it. Signing in appeared to succeed — the server really did create
 * the session — and the next navigation arrived with no cookie at all and
 * bounced to the sign-in form. The project's own Caddyfile spells that failure
 * out; the interface walked into it anyway, and it was reported four times as
 * "it keeps throwing me to login".
 *
 * So it is configuration, read once from the environment, exactly as the Go
 * side does it (`platform/config`, COOKIE_SECURE). It is deliberately not
 * inferred per request from a forwarded header: getting that inference wrong
 * drops the attribute in production, which is the more dangerous direction and
 * the one worth designing out.
 *
 * The default keeps a forgotten variable safe rather than convenient: a
 * production build assumes TLS. `make front-start` and the dev overlay say
 * otherwise explicitly, because they are the cases that genuinely have no
 * certificate.
 */
export function cookieSecure(): boolean {
  const configured = process.env.COOKIE_SECURE?.trim().toLowerCase();

  if (configured === "true" || configured === "1") return true;
  if (configured === "false" || configured === "0") return false;

  // Unset, or something nobody can read. Both fall back to the safe default:
  // reading "yes" as false would drop the attribute in production silently,
  // which is the failure this whole function exists to prevent.
  return process.env.NODE_ENV === "production";
}
