// @vitest-environment node
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

import { Linter } from "eslint";
import nextTs from "eslint-config-next/typescript";
import { afterAll, beforeAll, describe, expect, test } from "vitest";

import { noSchemaInClient } from "./no-schema-in-client.mjs";

/** A small project: an area split into a schema module and its terms, plus the shapes around them. */
const PROJECT: Record<string, string> = {
  "lib/api/area.ts": 'import { z } from "zod";\nimport { KINDS } from "./area-terms";\nexport const areaSchema = z.enum(KINDS);\nexport type Area = z.infer<typeof areaSchema>;\nexport const MAX = 3;\n',
  "lib/api/area-terms.ts": 'import type { Area } from "./area";\nexport const KINDS = ["a", "b"] as const;\nexport const pick = (area: Area) => area;\n',
  "lib/api/browser.ts": "export const browserRequest = async (path: string) => fetch(path);\n",
  "lib/api/live.ts": 'import { z } from "zod";\nimport { browserRequest } from "./browser";\nexport const fetchLive = async () => z.number().parse(await browserRequest("/live"));\nexport const LIVE_MAX = 5;\n',
  "lib/api/typed-only.ts": 'import type { Refused } from "./browser";\nimport { z } from "zod";\nexport const shape = z.string();\nexport type R = Refused;\n',
  "components/shared.tsx": 'import { MAX } from "@/lib/api/area";\nexport const Shared = () => MAX;\n',
  "components/plain.tsx": 'import { KINDS } from "@/lib/api/area-terms";\nexport const Plain = () => KINDS.length;\n',
  "app/page/actions.ts": '"use server";\nimport { areaSchema } from "@/lib/api/area";\nexport async function act() { return areaSchema.parse("a"); }\n',
  "app/page/other-client.tsx": '"use client";\nimport { MAX } from "@/lib/api/area";\nexport const Other = () => MAX;\n',
};

/** The parser the project lints with, rather than a second copy pinned here. */
const parser = nextTs.find((config) => config.languageOptions?.parser)!.languageOptions!.parser;

let root: string;

beforeAll(() => {
  root = mkdtempSync(join(tmpdir(), "no-schema-in-client-"));
  for (const [path, source] of Object.entries(PROJECT)) {
    mkdirSync(dirname(join(root, path)), { recursive: true });
    writeFileSync(join(root, path), source);
  }
});

afterAll(() => rmSync(root, { recursive: true, force: true }));

/** The rule's messages for `source` saved as `app/page/<name>`. */
function lint(source: string, name = "panel.tsx"): string[] {
  const linter = new Linter({ configType: "flat", cwd: root });
  const messages = linter.verify(
    source,
    [
      {
        files: ["**/*.ts", "**/*.tsx"],
        languageOptions: { parser },
        plugins: { bundle: { rules: { "no-schema-in-client": noSchemaInClient } } },
        rules: { "bundle/no-schema-in-client": ["error", { root }] },
      },
    ],
    { filename: join(root, "app/page", name) },
  );
  return messages.map((message) => message.message);
}

const CLIENT = '"use client";\n';

describe("no-schema-in-client", () => {
  test("refuses a client file's value import of a module that loads zod", () => {
    const messages = lint(`${CLIENT}import { MAX } from "@/lib/api/area";\nexport const n = MAX;\n`);

    expect(messages).toHaveLength(1);
    expect(messages[0]).toContain("lib/api/area.ts");
  });

  test.each(["zod", "zod/mini"])("refuses %s itself in a client file", (specifier) => {
    expect(lint(`${CLIENT}import { z } from "${specifier}";\nexport const s = z.string();\n`)).toHaveLength(1);
  });

  test("names the whole path when zod arrives through a shared module", () => {
    const messages = lint(`${CLIENT}import { Shared } from "@/components/shared";\nexport const n = Shared;\n`);

    expect(messages).toHaveLength(1);
    expect(messages[0]).toContain("components/shared.tsx -> lib/api/area.ts");
  });

  test("follows relative specifiers as well as the alias", () => {
    expect(lint(`${CLIENT}import { MAX } from "../../lib/api/area";\nexport const n = MAX;\n`)).toHaveLength(1);
  });

  test("refuses a value re-export of a schema module", () => {
    expect(lint(`${CLIENT}export { MAX } from "@/lib/api/area";\n`)).toHaveLength(1);
  });

  test.each([
    ["a type-only import", 'import type { Area } from "@/lib/api/area";\nexport type A = Area;\n'],
    ["an import whose names are all types", 'import { type Area } from "@/lib/api/area";\nexport type A = Area;\n'],
    ["a terms module, even one that names the schema's types", 'import { KINDS, pick } from "@/lib/api/area-terms";\nexport const k = [KINDS, pick];\n'],
    ["a zod-free shared module", 'import { Plain } from "@/components/plain";\nexport const p = Plain;\n'],
    ["a server action, which the bundle only references", 'import { act } from "./actions";\nexport const a = act;\n'],
    ["another client file, which answers for itself", 'import { Other } from "./other-client";\nexport const o = Other;\n'],
    ["a module that makes browser calls, whose answers need zod anyway", 'import { fetchLive, LIVE_MAX } from "@/lib/api/live";\nexport const l = [fetchLive, LIVE_MAX];\n'],
  ])("allows %s", (_, source) => {
    expect(lint(`${CLIENT}${source}`)).toEqual([]);
  });

  test("does not count a type-only import of the browser adapter as making browser calls", () => {
    expect(lint(`${CLIENT}import { shape } from "@/lib/api/typed-only";\nexport const s = shape;\n`)).toHaveLength(1);
  });

  test("leaves files without the client directive alone", () => {
    expect(lint('import { MAX } from "@/lib/api/area";\nexport const n = MAX;\n', "page.tsx")).toEqual([]);
  });
});
