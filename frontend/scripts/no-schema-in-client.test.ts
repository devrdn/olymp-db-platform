// @vitest-environment node
import { mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";

import { Linter } from "eslint";
import nextTs from "eslint-config-next/typescript";
import { afterAll, beforeAll, describe, expect, test } from "vitest";

import { noSchemaInClient } from "./no-schema-in-client.mjs";

const ZOD = 'import { z } from "zod";\n';
const BROWSER = 'import { browserRequest } from "./browser";\n';

/** A small project: an area split into a schema module and its terms, plus the shapes around them. */
const PROJECT: Record<string, string> = {
  "lib/api/area.ts": `${ZOD}import { KINDS } from "./area-terms";\nexport const areaSchema = z.enum(KINDS);\nexport type Area = z.infer<typeof areaSchema>;\nexport const MAX = 3;\n`,
  "lib/api/area-terms.ts": 'import type { Area } from "./area";\nexport const KINDS = ["a", "b"] as const;\nexport const pick = (area: Area) => area;\n',
  "lib/api/browser.ts": "export const browserRequest = async (path: string) => fetch(path);\n",
  "lib/api/live.ts": [
    ZOD,
    BROWSER,
    'export const fetchLive = async () => z.number().parse(await browserRequest("/live"));\n',
    'const read = () => browserRequest("/read");\n',
    "export const fetchThroughHelper = async () => z.number().parse(await read());\n",
    'export async function fetchDeclared() { return browserRequest("/declared"); }\n',
    "const local = fetchLive;\n",
    "export { local as fetchRenamed };\n",
    "export const LIVE_MAX = 5;\n",
    "export const liveHref = (id: string) => `/live/${id}`;\n",
    "export const typedOnly: typeof fetchLive | null = null;\n",
    "export const keyedOnly = { browserRequest: 1 };\n",
    "export const readsKey = (box: { browserRequest: number }) => box.browserRequest;\n",
    "export const shadowed = (browserRequest: number) => browserRequest + 1;\n",
    "export const callers = { fetchLive };\n",
    "export const generic = <browserRequest,>(value: browserRequest) => value;\n",
    "export const labelled = () => { browserRequest: for (;;) { break browserRequest; } };\n",
    "export const enumerated = () => { enum Kind { browserRequest } return Kind; };\n",
  ].join(""),
  "lib/api/typed-only.ts": `import type { Refused } from "./browser";\n${ZOD}export const shape = z.string();\nexport type R = Refused;\n`,
  "lib/cycle-a.ts": 'import { b } from "./cycle-b";\nexport const a = 1;\nexport const useB = () => b;\n',
  "lib/cycle-b.ts": `import { a } from "./cycle-a";\n${ZOD}export const b = z.string();\nexport const useA = () => a;\n`,
  "lib/calm-c.ts": 'import { d } from "./calm-d";\nexport const c = 1;\nexport const useD = () => d;\n',
  "lib/calm-d.ts": 'import { c } from "./calm-c";\nexport const d = 2;\nexport const useC = () => c;\n',
  "lib/notes.json": '{ "a": 1 }\n',
  "lib/swap.ts": "export const swap = 1;\n",
  "components/grow/index.ts": "export const grow = 1;\n",
  "components/shared.tsx": 'import { MAX } from "@/lib/api/area";\nexport const Shared = () => MAX;\n',
  "components/plain.tsx": 'import { KINDS } from "@/lib/api/area-terms";\nexport const Plain = () => KINDS.length;\n',
  "components/star.ts": 'export * from "@/lib/api/area";\n',
  "components/side-effect.ts": 'import "@/lib/api/area";\nexport const s = 1;\n',
  "components/folder/index.ts": 'export { MAX } from "@/lib/api/area";\n',
  "components/via-action.ts": 'export { act } from "@/app/page/actions";\n',
  "components/lazy.ts": 'export const load = () => import("@/lib/api/area");\n',
  "components/js-specifier.ts": 'export { MAX } from "../lib/api/area.js";\n',
  "components/type-reexport.ts": 'export { type Area } from "@/lib/api/area";\nexport const t = 1;\n',
  "components/data.ts": 'import notes from "@/lib/notes.json";\nexport const n = notes;\n',
  "app/page/actions.ts": '"use server";\nimport { areaSchema } from "@/lib/api/area";\nexport async function act() { return areaSchema.parse("a"); }\n',
  "app/page/other-client.tsx": '"use client";\nimport { MAX } from "@/lib/api/area";\nexport const Other = () => MAX;\n',
};

/** The parser the project lints with, rather than a second copy pinned here. */
const parser = nextTs.find((config) => config.languageOptions?.parser)!.languageOptions!.parser;

let base: string;
let root: string;

beforeAll(() => {
  base = mkdtempSync(join(tmpdir(), "no-schema-in-client-"));
  root = join(base, "project");
  for (const [path, source] of Object.entries(PROJECT)) {
    mkdirSync(dirname(join(root, path)), { recursive: true });
    writeFileSync(join(root, path), source);
  }
  // Outside the project: reached by climbing out, or through a link that points out.
  writeFileSync(join(base, "outside.ts"), `${ZOD}export const o = z.string();\n`);
  symlinkSync(join(base, "outside.ts"), join(root, "lib/linked.ts"));
});

afterAll(() => rmSync(base, { recursive: true, force: true }));

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

  test.each([
    ["a relative specifier", 'import { MAX } from "../../lib/api/area";\nexport const n = MAX;\n'],
    ["a value re-export", 'export { MAX } from "@/lib/api/area";\n'],
    ["a side-effect import", 'import "@/lib/api/area";\n'],
    ["a shared module that re-exports everything", 'import { MAX } from "@/components/star";\nexport const n = MAX;\n'],
    ["a shared module's side-effect import", 'import { s } from "@/components/side-effect";\nexport const n = s;\n'],
    ["a folder's index", 'import { MAX } from "@/components/folder";\nexport const n = MAX;\n'],
    ["a .js specifier naming a .ts file", 'import { MAX } from "@/components/js-specifier";\nexport const n = MAX;\n'],
    ["a constant from a module that makes browser calls", 'import { LIVE_MAX } from "@/lib/api/live";\nexport const l = LIVE_MAX;\n'],
    ["a helper that makes no call, from a module that does", 'import { liveHref } from "@/lib/api/live";\nexport const l = liveHref;\n'],
    ["the whole namespace of a module that makes browser calls", 'import * as live from "@/lib/api/live";\nexport const l = live;\n'],
    ["a value that names a call only in its type", 'import { typedOnly } from "@/lib/api/live";\nexport const l = typedOnly;\n'],
    ["a value whose key spells the adapter", 'import { keyedOnly } from "@/lib/api/live";\nexport const l = keyedOnly;\n'],
    ["a helper that reads a property spelled like the adapter", 'import { readsKey } from "@/lib/api/live";\nexport const l = readsKey;\n'],
    ["a helper whose parameter shadows the adapter", 'import { shadowed } from "@/lib/api/live";\nexport const l = shadowed;\n'],
    ["a helper whose type parameter is spelled like the adapter", 'import { generic } from "@/lib/api/live";\nexport const l = generic;\n'],
    ["a helper whose label is spelled like the adapter", 'import { labelled } from "@/lib/api/live";\nexport const l = labelled;\n'],
    ["a helper whose enum member is spelled like the adapter", 'import { enumerated } from "@/lib/api/live";\nexport const l = enumerated;\n'],
  ])("refuses zod reached through %s", (_, source) => {
    expect(lint(`${CLIENT}${source}`)).toHaveLength(1);
  });

  test("reports a value cycle once, by the path into it", () => {
    const messages = lint(`${CLIENT}import { a } from "@/lib/cycle-a";\nexport const n = a;\n`);

    expect(messages).toHaveLength(1);
    expect(messages[0]).toContain("lib/cycle-a.ts -> lib/cycle-b.ts");
  });

  test.each([
    ["a type-only import", 'import type { Area } from "@/lib/api/area";\nexport type A = Area;\n'],
    ["an import whose names are all types", 'import { type Area } from "@/lib/api/area";\nexport type A = Area;\n'],
    ["a terms module, even one that names the schema's types", 'import { KINDS, pick } from "@/lib/api/area-terms";\nexport const k = [KINDS, pick];\n'],
    ["a zod-free shared module", 'import { Plain } from "@/components/plain";\nexport const p = Plain;\n'],
    ["a zod-free value cycle", 'import { c } from "@/lib/calm-c";\nexport const n = c;\n'],
    ["a shared module's type-only re-export by name", 'import { t } from "@/components/type-reexport";\nexport const n = t;\n'],
    ["a data file", 'import { n } from "@/components/data";\nexport const m = n;\n'],
    ["a server action", 'import { act } from "./actions";\nexport const a = act;\n'],
    ["a server action reached through a shared module", 'import { act } from "@/components/via-action";\nexport const a = act;\n'],
    ["another client file, which answers for itself", 'import { Other } from "./other-client";\nexport const o = Other;\n'],
    ["a call made through the browser adapter", 'import { fetchLive } from "@/lib/api/live";\nexport const l = fetchLive;\n'],
    ["a call made through a local helper", 'import { fetchThroughHelper } from "@/lib/api/live";\nexport const l = fetchThroughHelper;\n'],
    ["a declared function that calls", 'import { fetchDeclared } from "@/lib/api/live";\nexport const l = fetchDeclared;\n'],
    ["a call exported under another name", 'import { fetchRenamed } from "@/lib/api/live";\nexport const l = fetchRenamed;\n'],
    ["a value that holds a call by shorthand", 'import { callers } from "@/lib/api/live";\nexport const l = callers;\n'],
    ["constants taken beside a call, which brings zod anyway", 'import { fetchLive, LIVE_MAX } from "@/lib/api/live";\nexport const l = [fetchLive, LIVE_MAX];\n'],
    ["a lazy import(), which is not first-load", 'export const load = () => import("@/lib/api/area");\n'],
    ["a shared module's lazy import()", 'import { load } from "@/components/lazy";\nexport const l = load;\n'],
  ])("allows %s", (_, source) => {
    expect(lint(`${CLIENT}${source}`)).toEqual([]);
  });

  test("does not count a type-only import of the browser adapter as making browser calls", () => {
    expect(lint(`${CLIENT}import { shape } from "@/lib/api/typed-only";\nexport const s = shape;\n`)).toHaveLength(1);
  });

  test.each([
    ["a missing file", 'import { x } from "./missing";\nexport const n = x;\n', "./missing"],
    ["a file outside the project", 'import { o } from "../../../outside";\nexport const n = o;\n', "../../../outside"],
    ["a link that leads outside the project", 'import { o } from "@/lib/linked";\nexport const n = o;\n', "@/lib/linked"],
  ])("reports a local import it cannot follow: %s", (_, source, specifier) => {
    const messages = lint(`${CLIENT}${source}`);

    expect(messages).toHaveLength(1);
    expect(messages[0]).toContain(`${specifier} cannot be followed`);
  });

  test("checks a remembered resolution again once the file turns into a link leading outside", () => {
    const source = `${CLIENT}import { swap } from "@/lib/swap";\nexport const n = swap;\n`;
    expect(lint(source)).toEqual([]);

    rmSync(join(root, "lib/swap.ts"));
    symlinkSync(join(base, "outside.ts"), join(root, "lib/swap.ts"));

    const messages = lint(source);
    expect(messages).toHaveLength(1);
    expect(messages[0]).toContain("@/lib/swap cannot be followed");
  });

  test("resolves again when a file appears ahead of the folder's index", () => {
    const source = `${CLIENT}import { grow } from "@/components/grow";\nexport const n = grow;\n`;
    expect(lint(source)).toEqual([]);

    writeFileSync(join(root, "components/grow.ts"), 'export { MAX as grow } from "@/lib/api/area";\n');

    const messages = lint(source);
    expect(messages).toHaveLength(1);
    expect(messages[0]).toContain("components/grow.ts -> lib/api/area.ts");
  });

  test("leaves files without the client directive alone", () => {
    expect(lint('import { MAX } from "@/lib/api/area";\nexport const n = MAX;\n', "page.tsx")).toEqual([]);
  });
});
