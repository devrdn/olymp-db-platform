import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative, resolve } from "node:path";

import { describe, expect, test } from "vitest";

import { AdminDictionary, AppDictionary, ArchiveDictionary, ParticipantDictionary, SessionDictionary } from "./client";

const ROOT = resolve(__dirname, "../..");

function sources(dir: string): string[] {
  return readdirSync(dir).flatMap((entry) => {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) return sources(full);
    return /\.tsx$/.test(entry) && !/\.test\.tsx$/.test(entry) ? [full] : [];
  });
}

/** Every module that puts a scope in context, whichever way it spells the provider. */
const PROVIDING = ["app", "components"]
  .flatMap((dir) => sources(join(ROOT, dir)))
  .filter((file) => /<\w+Dictionary(\.Provider|Provider)\b/.test(readFileSync(file, "utf8")))
  .map((file) => relative(ROOT, file));

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
    for (const scope of [AppDictionary, ParticipantDictionary, SessionDictionary, AdminDictionary, ArchiveDictionary]) {
      expect("select" in scope).toBe(false);
    }
  });

  test("every module that provides a scope narrows through the server module, never through the client one", () => {
    expect(PROVIDING).toEqual(expect.arrayContaining(["app/layout.tsx", "components/layout/frame.tsx"]));
    for (const file of PROVIDING) {
      const source = readFileSync(join(ROOT, file), "utf8");
      expect(source, file).not.toMatch(/Dictionary\.select\(/);
      expect(source, file).toMatch(/from "@\/lib\/i18n\/scopes"/);
    }
  });

  // Member access on a client reference is `undefined` on the server.
  test("every module that provides a scope renders a provider exported at the top level, never a member of a scope", () => {
    for (const file of PROVIDING) {
      const source = readFileSync(join(ROOT, file), "utf8");
      expect(source, file).not.toMatch(/<\w+Dictionary\.Provider\b/);
      expect(source, file).toMatch(/<\w+DictionaryProvider\b/);
    }
  });
});
