import { existsSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";

import ts from "typescript";

/**
 * Keeps zod out of the browser except where the browser parses an answer: a
 * `"use client"` file takes its constants from the area's `*-terms` module,
 * for the reason given in `lib/api/content-terms.ts`.
 *
 * The walk follows value imports through shared modules, because a component
 * without the directive is bundled with whoever imports it. It stops at
 * another client file (checked on its own), at a server action (the bundle
 * holds only a reference), and at a module that makes browser calls (imports
 * `lib/api/browser`): that screen parses the API's answers with zod, so zod
 * ships there whatever else is imported.
 *
 * Options: `{ root }`, the directory `@/` resolves against.
 */

const isZod = (specifier) => specifier === "zod" || specifier.startsWith("zod/");

const EXTENSIONS = ["", ".ts", ".tsx", ".mjs", ".js", "/index.ts", "/index.tsx"];

/** Each file's facts, keyed by path and modification time so an editor's long-lived linter stays current. */
const facts = new Map();

function resolveLocal(root, from, specifier) {
  let base;
  if (specifier.startsWith("@/")) base = join(root, specifier.slice(2));
  else if (specifier.startsWith(".")) base = resolve(dirname(from), specifier);
  else return null;
  for (const extension of EXTENSIONS) {
    const candidate = base + extension;
    if (existsSync(candidate) && statSync(candidate).isFile()) return candidate;
  }
  return null;
}

function isTypeOnlyImport(clause) {
  if (!clause) return false;
  if (clause.isTypeOnly) return true;
  const named = clause.namedBindings;
  return (
    !clause.name &&
    named !== undefined &&
    ts.isNamedImports(named) &&
    named.elements.length > 0 &&
    named.elements.every((element) => element.isTypeOnly)
  );
}

/** A module's directive and the specifiers it imports or re-exports as values. */
function readFacts(path) {
  const key = `${path}:${statSync(path).mtimeMs}`;
  const known = facts.get(key);
  if (known) return known;

  const source = ts.createSourceFile(path, readFileSync(path, "utf8"), ts.ScriptTarget.Latest, false);
  let directive = null;
  for (const statement of source.statements) {
    if (!ts.isExpressionStatement(statement) || !ts.isStringLiteral(statement.expression)) break;
    if (statement.expression.text === "use client" || statement.expression.text === "use server")
      directive = statement.expression.text;
  }
  const specifiers = [];
  for (const statement of source.statements) {
    if (ts.isImportDeclaration(statement) && !isTypeOnlyImport(statement.importClause))
      specifiers.push(statement.moduleSpecifier.text);
    else if (ts.isExportDeclaration(statement) && statement.moduleSpecifier && !statement.isTypeOnly)
      specifiers.push(statement.moduleSpecifier.text);
  }
  const result = { directive, specifiers };
  facts.set(key, result);
  return result;
}

/** The files from `path` to a zod import, or null when its value imports never reach zod. */
function pathToZod(root, path, seen) {
  if (seen.has(path)) return null;
  seen.add(path);

  const { directive, specifiers } = readFacts(path);
  if (directive !== null) return null;
  const targets = specifiers.map((specifier) => (isZod(specifier) ? "zod" : resolveLocal(root, path, specifier)));
  if (targets.includes(join(root, "lib/api/browser.ts"))) return null;
  if (targets.includes("zod")) return [path];
  for (const target of targets) {
    if (target === null) continue;
    const rest = pathToZod(root, target, seen);
    if (rest) return [path, ...rest];
  }
  return null;
}

const typeOnly = (node) =>
  node.importKind === "type" ||
  node.exportKind === "type" ||
  (node.specifiers?.length > 0 &&
    node.specifiers.every((specifier) => specifier.importKind === "type" || specifier.exportKind === "type"));

/** @type {import("eslint").Rule.RuleModule} */
export const noSchemaInClient = {
  meta: {
    type: "problem",
    docs: { description: "Forbids a client file's value imports that load zod outside a browser call." },
    schema: [{ type: "object", properties: { root: { type: "string" } }, required: ["root"], additionalProperties: false }],
    messages: {
      zod: "{{path}} loads zod into this client bundle. Take the constant from the area's *-terms module (see lib/api/content-terms.ts).",
    },
  },
  create(context) {
    const program = context.sourceCode.ast;
    const client = program.body.some((statement) => statement.directive === "use client");
    if (!client) return {};

    const { root } = context.options[0];
    const check = (node) => {
      if (!node.source || typeOnly(node)) return;
      const specifier = node.source.value;
      let found;
      if (isZod(specifier)) found = ["zod"];
      else {
        const target = resolveLocal(root, context.filename, specifier);
        const chain = target && pathToZod(root, target, new Set());
        found = chain && chain.map((file) => relative(root, file));
      }
      if (found) context.report({ node, messageId: "zod", data: { path: found.join(" -> ") } });
    };
    return { ImportDeclaration: check, ExportNamedDeclaration: check, ExportAllDeclaration: check };
  },
};
