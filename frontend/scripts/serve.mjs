#!/usr/bin/env node
/**
 * Serving the production build locally.
 *
 * `next start` does not serve a standalone build — Next says so itself, in a
 * warning that is easy to read past because the server starts anyway and then
 * answers without its static chunks. This project builds standalone because
 * that is what the container ships, and the point of running the production
 * build locally is to see what the container will do.
 *
 * `next build` splits that output three ways and the server only knows about
 * one of them: the traced server sits in `.next/standalone`, the static chunks
 * it references stay in `.next/static`, and `public` is untouched. The
 * Dockerfile copies all three into one tree; this does the same thing on the
 * host, so the two are the same arrangement rather than two guesses about it.
 */

import { cpSync, existsSync, rmSync } from "node:fs";
import { spawn } from "node:child_process";
import { join } from "node:path";

const STANDALONE = ".next/standalone";

if (!existsSync(join(STANDALONE, "server.js"))) {
  console.error(`No standalone build at ${STANDALONE}. Run "npm run build" first.`);
  process.exit(1);
}

/** Replaced rather than merged: a stale chunk served from a previous build is
 *  the kind of failure that looks like a code bug for an hour. */
function stage(from, to) {
  if (!existsSync(from)) return;
  rmSync(to, { recursive: true, force: true });
  cpSync(from, to, { recursive: true });
}

stage(".next/static", join(STANDALONE, ".next/static"));
stage("public", join(STANDALONE, "public"));
// The container's entry point and the rule it reads, placed where the
// Dockerfile places them, so a local start goes through the same INGRESS_SECRET
// check. `make front-start` and `npm run smoke` pass ALLOW_MISSING_INGRESS_SECRET=true,
// because they serve the build with no proxy in front.
cpSync("scripts/start.mjs", join(STANDALONE, "start.mjs"));
cpSync("lib/api/ingress-secret.mjs", join(STANDALONE, "ingress-secret.mjs"));

const server = spawn(process.execPath, [join(STANDALONE, "start.mjs")], {
  stdio: "inherit",
  env: { ...process.env, NODE_ENV: "production" },
});

server.on("exit", (code) => process.exit(code ?? 0));
