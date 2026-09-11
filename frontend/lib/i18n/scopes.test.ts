import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, test } from "vitest";

import { AdminDictionary, AppDictionary, ParticipantDictionary, SessionDictionary } from "./client";

/**
 * A dictionary is narrowed on the server, so the sections nobody downstream
 * can read never cross the wire. The narrowing therefore has to be a function
 * a Server Component can call, and a Server Component cannot call anything
 * exported from a "use client" module. It receives a client reference there,
 * not the function: property access on it yields another reference, and
 * calling that throws `TypeError: ... is not a function`.
 *
 * That is exactly how every page of the product went down. `select` lived on
 * the scope object in `client.tsx`, and the root, admin, participant and
 * session layouts all called it. The unit tests were green because vitest
 * imports a "use client" module as plain JavaScript and never enforces the
 * boundary; `next build` was green because no dynamic route is rendered at
 * build time. The first request to /login answered 500.
 *
 * So these tests pin the boundary itself, which is the one thing a jsdom run
 * cannot see for us.
 */
describe("dictionary scopes and the server boundary", () => {
  test("the module a Server Component narrows with is not a client module", () => {
    const source = readFileSync(resolve(__dirname, "scopes.ts"), "utf8").trimStart();
    expect(source.startsWith('"use client"')).toBe(false);
    expect(source.startsWith("'use client'")).toBe(false);
  });

  test("no scope in the client module offers a method a Server Component would call", () => {
    for (const scope of [AppDictionary, ParticipantDictionary, SessionDictionary, AdminDictionary]) {
      expect("select" in scope).toBe(false);
    }
  });

  test("every route layout narrows through the server module, never through the client one", () => {
    const layouts = [
      "app/layout.tsx",
      "app/(admin)/layout.tsx",
      "app/(participant)/layout.tsx",
      "app/(session)/layout.tsx",
    ];
    for (const layout of layouts) {
      const source = readFileSync(resolve(__dirname, "../..", layout), "utf8");
      expect(source, layout).not.toMatch(/Dictionary\.select\(/);
      expect(source, layout).toMatch(/from "@\/lib\/i18n\/scopes"/);
    }
  });

  /**
   * The same boundary, one step later. With `select` fixed, the root layout
   * still answered 500 — "Element type is invalid ... got: undefined" — because
   * it rendered `<AppDictionary.Provider>`. Member access on a client
   * reference does not yield a component on the server; only a top-level
   * export of the "use client" module is a reference React can render. The
   * `select` failure had simply thrown first, being evaluated as a prop.
   */
  test("every route layout renders a provider exported at the top level, never a member of a scope", () => {
    const layouts = [
      "app/layout.tsx",
      "app/(admin)/layout.tsx",
      "app/(participant)/layout.tsx",
      "app/(session)/layout.tsx",
    ];
    for (const layout of layouts) {
      const source = readFileSync(resolve(__dirname, "../..", layout), "utf8");
      expect(source, layout).not.toMatch(/<\w+Dictionary\.Provider\b/);
      expect(source, layout).toMatch(/<\w+DictionaryProvider\b/);
    }
  });
});
