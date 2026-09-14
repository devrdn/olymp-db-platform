// @vitest-environment node
import { spawnSync } from "node:child_process";
import { copyFileSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { afterEach, describe, expect, test } from "vitest";

import { ALLOW_MISSING_FLAG, startupRefusal } from "../lib/api/ingress-secret.mjs";

/**
 * The interface's server hands the browser's address to the API only on a
 * request Caddy vouched for with INGRESS_SECRET. Without a usable secret it
 * hands nothing on, and behind Caddy every visitor then counts as the web
 * container — one login budget for the whole installation, with the site
 * otherwise working. So a production server refuses to start without one,
 * and the refusal lives in the process's own entry point rather than in the
 * compose file, where it would stop every development command too.
 */

const SECRET = "an-ingress-secret-of-at-least-32-characters";
const PLACEHOLDER = "CHANGE-ME-to-the-output-of-openssl-rand-hex-32";

describe("startupRefusal", () => {
  test.each([
    ["missing", undefined],
    ["empty", ""],
    ["too short", "tiny-secret"],
    ["the example file's placeholder", PLACEHOLDER],
  ])("refuses a production server whose secret is %s, naming only the variable", (_, value) => {
    const refusal = startupRefusal({ NODE_ENV: "production", INGRESS_SECRET: value });

    expect(refusal).toMatch(/INGRESS_SECRET/);
    if (value) expect(refusal).not.toContain(value);
    expect(refusal?.toLowerCase()).not.toContain("change-me");
  });

  test("starts a production server with a real secret", () => {
    expect(startupRefusal({ NODE_ENV: "production", INGRESS_SECRET: SECRET })).toBeNull();
  });

  test("starts a development server without one", () => {
    expect(startupRefusal({ NODE_ENV: "development" })).toBeNull();
  });

  test("starts a local production build without one only when told to explicitly", () => {
    // `make front-start` and `npm run smoke` serve the production build with
    // no proxy in front, where there is nothing for a secret to prove.
    expect(startupRefusal({ NODE_ENV: "production", [ALLOW_MISSING_FLAG]: "true" })).toBeNull();
    expect(startupRefusal({ NODE_ENV: "production", [ALLOW_MISSING_FLAG]: "yes" })).not.toBeNull();
  });
});

/**
 * The entry point itself, run as the container runs it: start.mjs next to the
 * rule it reads and a server.js, here a stand-in that only says it started.
 */
describe("start.mjs", () => {
  let dir: string;

  afterEach(() => {
    if (dir) rmSync(dir, { recursive: true, force: true });
  });

  function start(env: Record<string, string>) {
    dir = mkdtempSync(join(tmpdir(), "web-start-"));
    copyFileSync("scripts/start.mjs", join(dir, "start.mjs"));
    copyFileSync("lib/api/ingress-secret.mjs", join(dir, "ingress-secret.mjs"));
    writeFileSync(join(dir, "server.js"), 'console.log("server started");\n');

    const { PATH } = process.env;
    return spawnSync(process.execPath, [join(dir, "start.mjs")], {
      // Only what is given: the test's own environment must not decide it.
      env: { PATH: PATH ?? "", ...env } as unknown as NodeJS.ProcessEnv,
      encoding: "utf8",
    });
  }

  test("exits non-zero before the server starts when production has no secret", () => {
    const run = start({ NODE_ENV: "production" });

    expect(run.status).not.toBe(0);
    expect(run.stderr).toMatch(/INGRESS_SECRET/);
    expect(run.stdout).not.toContain("server started");
  });

  test("exits non-zero on the placeholder, without printing it", () => {
    const run = start({ NODE_ENV: "production", INGRESS_SECRET: PLACEHOLDER });

    expect(run.status).not.toBe(0);
    expect(run.stderr + run.stdout).not.toContain(PLACEHOLDER);
    expect(run.stdout).not.toContain("server started");
  });

  test("starts the server with a real secret", () => {
    const run = start({ NODE_ENV: "production", INGRESS_SECRET: SECRET });

    expect(run.status).toBe(0);
    expect(run.stdout).toContain("server started");
  });

  test("starts the server locally when told to explicitly", () => {
    const run = start({ NODE_ENV: "production", [ALLOW_MISSING_FLAG]: "true" });

    expect(run.status).toBe(0);
    expect(run.stdout).toContain("server started");
  });
});

/** The container must start through the check, not around it. */
describe("the container image", () => {
  const dockerfile = readFileSync("Dockerfile", "utf8");

  test("starts start.mjs rather than server.js directly", () => {
    expect(dockerfile).toMatch(/^CMD \["node", "start\.mjs"\]$/m);
  });

  test("ships start.mjs and the rule it reads next to server.js", () => {
    expect(dockerfile).toMatch(/COPY --from=build \/src\/scripts\/start\.mjs \.\/start\.mjs/);
    expect(dockerfile).toMatch(/COPY --from=build \/src\/lib\/api\/ingress-secret\.mjs \.\/ingress-secret\.mjs/);
  });
});
