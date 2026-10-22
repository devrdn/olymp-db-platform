import type { NextConfig } from "next";

import { apiOrigin } from "./lib/api/config";
import { API_PREFIX } from "./lib/api/client";

const isDevelopment = process.env.NODE_ENV !== "production";

/**
 * Security headers on every document. One origin behind the reverse proxy
 * makes a `SameSite=Lax` cookie enough against CSRF; these cover framing,
 * sniffing, referrer leakage and script origins. Set here, not in the reverse
 * proxy, so they travel with the app whatever proxy is in front.
 */
const securityHeaders = [
  /**
   * Nothing is meant to be framed; a framed login enables clickjacking.
   * `frame-ancestors` is the modern form, X-Frame-Options covers older
   * browsers.
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
 * Production only: React's dev stack traces use `eval` and hot reload uses a
 * WebSocket, and loosening the real policy for them is how `unsafe-eval` gets
 * shipped.
 */
const contentSecurityPolicy = [
  "default-src 'self'",
  /**
   * `unsafe-inline` covers Next's inlined bootstrap. The stricter nonce plus
   * `strict-dynamic` approach is described in frontend/README.md.
   */
  "script-src 'self' 'unsafe-inline'",
  // Tailwind emits a stylesheet, but next/font inlines its @font-face rules.
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

/**
 * Production only, beside the policy: the development server is plain http
 * and must never ask a browser to pin localhost to https. A plain-http
 * `make front-start` sends it too, harmlessly, since a browser honours the
 * header only on a TLS response. One year, including subdomains, is the floor
 * the preload list accepts.
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
      : [
          ...securityHeaders,
          { key: "Content-Security-Policy", value: contentSecurityPolicy },
          { key: "Strict-Transport-Security", value: strictTransportSecurity },
        ];

    return [{ source: "/:path*", headers }];
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
