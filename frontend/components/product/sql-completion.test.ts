import { CompletionContext, type Completion, type CompletionResult } from "@codemirror/autocomplete";
import { PostgreSQL, sql } from "@codemirror/lang-sql";
import { ensureSyntaxTree } from "@codemirror/language";
import { EditorState } from "@codemirror/state";
import { describe, expect, test } from "vitest";

import type { GameSchema } from "@/lib/api/schema";

import { sqlCompletion } from "./sql-completion";

/**
 * The options every source offers for `doc` with the caret at `|`, without a
 * view: the popup itself is tested through `CodeEditor`.
 */
async function optionsAt(doc: string, schema: GameSchema | null): Promise<Completion[]> {
  const pos = doc.indexOf("|");
  const completion = sqlCompletion();
  completion.setSchema(schema);
  const state = EditorState.create({
    doc: doc.replace("|", ""),
    extensions: [sql({ dialect: PostgreSQL, upperCaseKeywords: true }), completion.extension],
  });
  ensureSyntaxTree(state, state.doc.length);
  const context = new CompletionContext(state, pos, false);
  const results = await Promise.all(
    state.languageDataAt<(context: CompletionContext) => CompletionResult | null>("autocomplete", pos).map(
      (source) => source(context),
    ),
  );
  return results.flatMap((result) => result?.options ?? []);
}

async function labelsAt(doc: string, schema: GameSchema | null): Promise<string[]> {
  return (await optionsAt(doc, schema)).map((option) => option.label);
}

const SCHEMA: GameSchema = {
  tables: [
    {
      name: "suspects",
      columns: [
        { name: "id", type: "integer", nullable: false, references: "" },
        { name: "order", type: "integer", nullable: false, references: "" },
      ],
    },
    {
      name: "alibis",
      columns: [
        { name: "id", type: "uuid", nullable: false, references: "" },
        { name: "place", type: "text", nullable: true, references: "" },
      ],
    },
    { name: "CaseFiles", columns: [{ name: "Shelf", type: "text", nullable: true, references: "" }] },
    { name: "v1.2", columns: [{ name: "note", type: "text", nullable: true, references: "" }] },
  ],
  truncated: false,
};

describe("the schema's names", () => {
  test("a table whose name holds a dot is one table, not a schema and a table", async () => {
    const options = await optionsAt("SELECT * FROM v|", SCHEMA);

    expect(options.find((option) => option.label === "v1.2")?.apply).toBe('"v1.2"');
  });

  test("a column named by a keyword is written quoted, with its type beside it", async () => {
    const options = await optionsAt("SELECT suspects.or|", SCHEMA);

    expect(options.find((option) => option.label === "order")).toMatchObject({
      apply: '"order"',
      detail: "integer",
    });
  });

  test("after an alias and a dot, only that table's columns", async () => {
    const labels = await labelsAt("SELECT a.| FROM alibis a", SCHEMA);

    expect(labels).toEqual(expect.arrayContaining(["id", "place"]));
    expect(labels).not.toContain("order");
  });
});

describe("columns without a table in front", () => {
  test("come from every table the statement names, each name once", async () => {
    const labels = await labelsAt("SELECT i| FROM suspects JOIN alibis USING (id)", SCHEMA);

    expect(labels.filter((label) => label === "id")).toHaveLength(1);
    expect(labels).toEqual(expect.arrayContaining(["order", "place"]));
  });

  test("come from a table named in a subquery too", async () => {
    const labels = await labelsAt("SELECT p| FROM suspects WHERE id IN (SELECT 1 FROM alibis)", SCHEMA);

    expect(labels).toContain("place");
  });

  test("an unquoted mixed-case name folds to lower case, as PostgreSQL reads it", async () => {
    expect(await labelsAt("SELECT S| FROM CaseFiles", SCHEMA)).not.toContain("Shelf");
    expect(await labelsAt('SELECT S| FROM "CaseFiles"', SCHEMA)).toContain("Shelf");
  });

  test("ignore the statements around the one being written", async () => {
    const labels = await labelsAt("SELECT * FROM alibis;\nSELECT p| FROM suspects", SCHEMA);

    expect(labels).not.toContain("place");
  });
});

describe("without a schema", () => {
  test("offers keywords and functions, and no name from the game", async () => {
    const labels = await labelsAt("SELECT * FROM suspects WHERE p|", null);

    expect(labels).toEqual(expect.arrayContaining(["POSITION", "PI"]));
    expect(labels).not.toContain("place");
    expect(labels).not.toContain("suspects");
  });
});
