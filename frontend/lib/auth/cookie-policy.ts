/**
 * Whether the session cookie is marked `Secure`, from `COOKIE_SECURE` as on
 * the Go side. TLS is a property of the deployment, not of NODE_ENV: a
 * production build served over plain http (`make front-start`) would have its
 * Secure cookie silently discarded. Never inferred from a forwarded header,
 * since a wrong guess would drop the attribute in production. Unset defaults
 * to Secure in production builds.
 */
export function cookieSecure(): boolean {
  const configured = process.env.COOKIE_SECURE?.trim().toLowerCase();

  if (configured === "true" || configured === "1") return true;
  if (configured === "false" || configured === "0") return false;

  // Unset or unreadable: the safe default, so a typo never drops the attribute.
  return process.env.NODE_ENV === "production";
}
