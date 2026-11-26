import type { NextConfig } from "next";

import { apiOrigin } from "./lib/api/config";
import { API_PREFIX } from "./lib/api/client";
import { STATIC_CONTENT_SECURITY_POLICY, UNPROXIED_PATHS } from "./lib/csp";

const isDevelopment = process.env.NODE_ENV !== "production";

/**
 * Security headers on every document. One origin behind the reverse proxy
 * makes a `SameSite=Lax` cookie enough against CSRF; these cover framing,
 * sniffing and referrer leakage. Set here, not in the reverse proxy, so they
 * travel with the app whatever proxy is in front. The content security policy
 * is not among them: proxy.ts sends a screen's around a nonce minted per
 * request, and only the paths it skips get a static one here.
 */
const securityHeaders = [
  /**
   * Nothing is meant to be framed; a framed login enables clickjacking.
   * The policy's `frame-ancestors` is the modern form, X-Frame-Options
   * covers older browsers and the development server, which sends no policy.
   */
  { key: "X-Frame-Options", value: "DENY" },
  { key: "X-Content-Type-Options", value: "nosniff" },
  /** Contest URLs carry identifiers; outside sites get the origin only. */
  { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
  /** No feature needs camera, microphone or location. */
  {
    key: "Permissions-Policy",
    value: "camera=(), microphone=(), geolocation=(), interest-cohort=()",
  },
];

/**
 * Production only: the development server is plain http and must never ask a
 * browser to pin localhost to https. A plain-http `make front-start` sends it
 * too, harmlessly, since a browser honours the header only on a TLS response.
 * One year, including subdomains, is the floor the preload list accepts.
 */
const strictTransportSecurity = "max-age=31536000; includeSubDomains";

const nextConfig: NextConfig = {
  /**
   * A self-contained server with only the dependencies it uses, so the image
   * needs no package manager.
   */
  output: "standalone",

  /** Do not advertise the framework. */
  poweredByHeader: false,

  /**
   * Nothing renders `next/image`: every picture is an `<img>` on purpose,
   * because the API already crops, resizes and re-encodes what it serves.
   * Without this the optimizer still mounts `/_next/image`, an unauthenticated
   * endpoint that decodes whatever bytes it is pointed at.
   */
  images: { unoptimized: true },

  /**
   * Caddy's `encode zstd gzip` (deploy/Caddyfile) compresses every response
   * on its own cores; this single Node process should not gzip the same bytes
   * first. Behind no proxy a response is merely uncompressed.
   */
  compress: false,

  async headers() {
    const headers = isDevelopment
      ? securityHeaders
      : [...securityHeaders, { key: "Strict-Transport-Security", value: strictTransportSecurity }];
    /**
     * Production only, as the proxy's is. This rule matches in any letter
     * case and the proxy's matcher in one, so `/Brand/x` meets both; Next
     * applies the proxy's response headers last, so the nonce policy wins.
     */
    const unproxied = isDevelopment
      ? []
      : [
          {
            // Unnamed groups: a named one would have Next compile the value against it.
            source: `/(${UNPROXIED_PATHS.join("|")})(.*)`,
            headers: [{ key: "content-security-policy", value: STATIC_CONTENT_SECURITY_POLICY }],
          },
        ];

    return [
      { source: "/:path*", headers },
      ...unproxied,
      /**
       * A reset link carries its token in the address, so nothing the page
       * requests may repeat it. Last, because a later rule's header wins.
       */
      { source: "/reset", headers: [{ key: "Referrer-Policy", value: "no-referrer" }] },
    ];
  },

  /**
   * Serves `/api/*` when no reverse proxy is in front (e.g. `make front-start`,
   * a production build). The browser fetches settings images itself, from the
   * page's origin, and without this they would get an HTML 404. Behind a proxy
   * the rule never fires, since the proxy takes `/api/*` first.
   */
  async rewrites() {
    /**
     * `apiOrigin()` refuses to guess in production, but this also runs at build
     * time where the address is unknown; then no rule is added, and server-side
     * calls still fail loudly at run time.
     */
    let origin: string;
    try {
      origin = apiOrigin();
    } catch {
      return [];
    }

    return [{ source: `${API_PREFIX}/:path*`, destination: `${origin}${API_PREFIX}/:path*` }];
  },
};

export default nextConfig;
