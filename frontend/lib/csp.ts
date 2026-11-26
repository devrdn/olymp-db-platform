/**
 * The content security policy, stated once. A screen is served under
 * {@link contentSecurityPolicy} with a nonce proxy.ts mints per request.
 * {@link STATIC_CONTENT_SECURITY_POLICY} covers the rest: proxy.ts sends it
 * on `/api/*`, and next.config.ts on the paths the proxy never runs on.
 */

/**
 * The request headers Next takes a script nonce from
 * (`next/dist/server/app-render/get-script-nonce-from-header`). A browser
 * that sent one would choose the nonce its page is stamped with.
 */
export const NONCE_REQUEST_HEADERS = ["content-security-policy", "content-security-policy-report-only"];

/**
 * The path prefixes, after the leading slash, that proxy.ts's matcher skips,
 * as regular-expression fragments. Next reads the matcher only as a literal,
 * so proxy.ts spells it out and next.config.test.ts holds it to this list.
 */
export const UNPROXIED_PATHS = [
  "_next/static",
  "_next/image",
  "favicon\\.ico",
  "icon\\.svg",
  "apple-icon\\.png",
  "robots\\.txt",
  "sitemap\\.xml",
  "brand/",
];

/** Sixteen random bytes in standard base64, the form Next's nonce pattern accepts. */
export function newNonce(): string {
  return Buffer.from(crypto.getRandomValues(new Uint8Array(16))).toString("base64");
}

function policy(scripts: string): string {
  return [
    "default-src 'self'",
    `script-src ${scripts}`,
    /**
     * No nonce here: one would switch `'unsafe-inline'` off, and CodeMirror
     * injects `<style>` elements while React renders `style` attributes (the
     * pane widths, global-error).
     */
    "style-src 'self' 'unsafe-inline'",
    "img-src 'self' data: blob:",
    "font-src 'self' data:",
    // Same-origin only: the API is reached through this origin's /api prefix.
    "connect-src 'self'",
    "form-action 'self'",
    "frame-ancestors 'none'",
    "base-uri 'none'",
    "object-src 'none'",
  ].join("; ");
}

/** `'self'` is for a browser too old for `'strict-dynamic'`, which ignores it otherwise. */
export function contentSecurityPolicy(nonce: string): string {
  return policy(`'self' 'nonce-${nonce}' 'strict-dynamic'`);
}

/**
 * For the paths no nonce is minted for: assets, and the not-found page Next
 * renders under them or under `/api/*`. With no nonce no inline script runs,
 * so such a page shows but does not hydrate.
 */
export const STATIC_CONTENT_SECURITY_POLICY = policy("'self'");
