import type { NextConfig } from "next";

import { apiOrigin } from "./lib/api/config";
import { API_PREFIX } from "./lib/api/client";

/**
 * Headers the interface sends on every document.
 *
 * The application and the API share one origin behind the reverse proxy, which
 * is what makes a `SameSite=Lax` session cookie enough against CSRF. These
 * cover what that does not: framing, sniffing, referrer leakage, and script
 * origins. They are set here rather than in the proxy so they travel with the
 * application — a deployment that puts a different proxy in front does not
 * silently lose them.
 */
const isDevelopment = process.env.NODE_ENV !== "production";

const securityHeaders = [
  /**
   * Nothing in this product is meant to be embedded, and the sign-in form is
   * the reason to say so: a framed login is the clickjacking primitive.
   * `frame-ancestors` is the modern spelling; X-Frame-Options is kept for the
   * browsers that only read that one.
   */
  { key: "X-Frame-Options", value: "DENY" },
  { key: "X-Content-Type-Options", value: "nosniff" },
  /**
   * A contest URL carries the contest's identifier. Sending the full path to
   * whatever a participant clicks through to is more than an outside site needs
   * to know; the origin is enough for the analytics anybody legitimately has.
   */
  { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
  /** No feature here needs a camera, a microphone or a location. */
  {
    key: "Permissions-Policy",
    value: "camera=(), microphone=(), geolocation=(), interest-cohort=()",
  },
];

/**
 * The content policy, in production only.
 *
 * A development server is not a security boundary, and a strict policy there
 * buys nothing while breaking the things development is made of: React rebuilds
 * stack traces with `eval`, and hot reload runs over a WebSocket. Loosening the
 * real policy so those keep working is how `unsafe-eval` ends up shipped, so
 * the policy is simply not sent where it protects nobody.
 */
const contentSecurityPolicy = [
  "default-src 'self'",
  /**
   * `unsafe-inline` covers Next's inlined bootstrap. The stricter shape is a
   * per-request nonce minted in proxy.ts together with `strict-dynamic`; it is
   * worth doing and is written down in the README rather than left as a comment
   * nobody finds.
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


const nextConfig: NextConfig = {
  /**
   * A self-contained server plus only the dependencies it actually reached for,
   * which is what lets the image be a runtime with no package manager in it.
   */
  output: "standalone",

  /** The framework and its version are not something a visitor needs told. */
  poweredByHeader: false,

  async headers() {
    const headers = isDevelopment
      ? securityHeaders
      : [...securityHeaders, { key: "Content-Security-Policy", value: contentSecurityPolicy }];

    return [{ source: "/:path*", headers }];
  },

  /**
   * The one origin, wherever this runs.
   *
   * Almost everything the interface asks the API for is asked by the Next
   * server, which dials the API directly. The settings images are the
   * exception: they are an `<img>` src, so the *browser* fetches them, from
   * whatever origin the page came from. Behind the reverse proxy that is the
   * proxy, which owns `/api/*` and forwards it. Run without one — which
   * `make front-start` does, and it is a production build, so gating this on
   * the environment would not have helped — the request lands on this
   * application, which does not serve that prefix, and every settings image
   * answers with an HTML 404 instead of a picture.
   *
   * So the application carries the route itself, for the same reason the
   * security headers above are set here rather than in the proxy: a deployment
   * that fronts this differently should not silently lose a feature. Behind a
   * proxy this costs nothing, because the proxy takes `/api/*` before the
   * request ever reaches Next.
   */
  async rewrites() {
    /**
     * `apiOrigin()` refuses to guess in production, which is right for a
     * request that has to arrive somewhere and wrong here: this also runs at
     * build time, where the address is neither known nor needed. An unknown
     * address means no rule — the deployment that hid it is the one with a
     * proxy in front — and a server-side call still fails loudly at run time.
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
