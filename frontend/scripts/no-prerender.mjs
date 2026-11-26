#!/usr/bin/env node
/**
 * Fails the build when `next build` rendered a page ahead of any request.
 *
 * proxy.ts serves every screen under a policy whose script nonce is minted per
 * request, and Next stamps that nonce on a page's scripts while it renders the
 * page. A page rendered at build time has no request, so its scripts carry no
 * nonce and the browser blocks them all: the page shows and never hydrates,
 * and nothing in the build says so. Every route is dynamic today because the
 * root layout reads cookies; this catches the route that stops being.
 *
 * `/_global-error`, the framework's own error shell, is the one page the build
 * always renders.
 *
 * Run from the frontend directory, after `next build`.
 */

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

const MANIFEST = ".next/prerender-manifest.json";
const ALWAYS_RENDERED = new Set(["/_global-error"]);

/**
 * The routes of a prerender manifest rendered at build time that should not
 * be. A route handler (`routeType: "route"`) is passed, since it serves no page
 * and so no script; an entry of any other or unknown kind is named.
 */
export function prerenderedPages(manifest) {
  return [...Object.entries(manifest.routes ?? {}), ...Object.entries(manifest.dynamicRoutes ?? {})]
    .filter(([route, entry]) => entry?.routeType !== "route" && !ALWAYS_RENDERED.has(route))
    .map(([route]) => route);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const pages = prerenderedPages(JSON.parse(readFileSync(MANIFEST, "utf8")));
  if (pages.length > 0) {
    console.error(
      `no-prerender: these pages were rendered at build time, so their scripts carry no nonce and will not run under the content policy:\n  ${pages.join("\n  ")}`,
    );
    console.error("Make each one dynamic (it reads cookies, headers or `connection()`).");
    process.exit(1);
  }
}
