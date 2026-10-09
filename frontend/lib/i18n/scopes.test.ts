import { readFileSync } from "node:fs";
import { resolve } from "node:path";

import { describe, expect, test } from "vitest";

import { AdminDictionary, AppDictionary, ParticipantDictionary, SessionDictionary } from "./client";

/**
 * A Server Component cannot call or render a member of a "use client" module.
 * Vitest imports such a module as plain JavaScript and `next build` renders no
 * dynamic route, so neither catches it; these tests pin the boundary instead.
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

  // Member access on a client reference is `undefined` on the server.
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
