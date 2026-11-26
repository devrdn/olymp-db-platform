// @vitest-environment node
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { afterEach, describe, expect, test } from "vitest";

import { prerenderedPages } from "./no-prerender.mjs";

type Entry = { routeType?: string };

const entries = (routes: (string | [string, Entry])[]) =>
  Object.fromEntries(routes.map((route) => (typeof route === "string" ? [route, { routeType: "page" }] : route)));

const manifest = (routes: (string | [string, Entry])[], dynamicRoutes: (string | [string, Entry])[] = []) => ({
  version: 4,
  routes: entries(routes),
  dynamicRoutes: entries(dynamicRoutes),
  notFoundRoutes: [],
});

describe("prerenderedPages", () => {
  test("passes a build whose only rendered page is the framework's error shell", () => {
    expect(prerenderedPages(manifest(["/_global-error"]))).toEqual([]);
  });

  test("names a page that became static", () => {
    expect(prerenderedPages(manifest(["/_global-error", "/login"]))).toEqual(["/login"]);
  });

  // A route handler answers without a page, so it has no script to stamp.
  test("passes a static route handler", () => {
    expect(prerenderedPages(manifest(["/_global-error", ["/robots.txt", { routeType: "route" }]]))).toEqual([]);
  });

  test("names an entry of a kind it does not know", () => {
    expect(prerenderedPages(manifest(["/_global-error", ["/login", {}]]))).toEqual(["/login"]);
  });

  test("names a dynamic route rendered ahead for its parameters", () => {
    expect(prerenderedPages(manifest(["/_global-error"], ["/contests/[contestId]"]))).toEqual([
      "/contests/[contestId]",
    ]);
  });
});

describe("the build step", () => {
  let base: string | undefined;

  afterEach(() => {
    if (base) rmSync(base, { recursive: true, force: true });
    base = undefined;
  });

  function run(routes: string[]) {
    base = mkdtempSync(join(tmpdir(), "no-prerender-"));
    mkdirSync(join(base, ".next"));
    writeFileSync(join(base, ".next", "prerender-manifest.json"), JSON.stringify(manifest(routes)));
    return spawnSync(process.execPath, [join(import.meta.dirname, "no-prerender.mjs")], { cwd: base, encoding: "utf8" });
  }

  test("fails the build on a prerendered page, naming it", () => {
    const result = run(["/_global-error", "/login"]);

    expect(result.status).toBe(1);
    expect(result.stderr).toContain("/login");
  });

  test("passes a build that rendered nothing ahead", () => {
    expect(run(["/_global-error"]).status).toBe(0);
  });
});
