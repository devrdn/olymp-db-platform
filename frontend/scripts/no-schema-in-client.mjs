import { readFileSync, realpathSync, statSync } from "node:fs";
import { dirname, extname, join, relative, resolve, sep } from "node:path";

import ts from "typescript";

/**
 * Keeps zod out of the browser except where the browser parses an answer: a
 * `"use client"` file takes its constants from the area's `*-terms` module,
 * for the reason given in `lib/api/content-terms.ts`.
 *
 * The walk follows value imports through shared modules, because a component
 * without the directive is bundled with whoever imports it. It stops at
 * another client file (checked on its own), at a server action (the bundle
 * holds only a reference), and at an import that takes a function which calls
 * the browser adapter (`lib/api/browser`): that call parses the API's answer
 * with zod, so zod ships with it whatever else is imported. A constant taken
 * alone from the same module is walked like any other import. A lazy
 * `import()` is not followed: it loads on demand, not with the page.
 *
 * A local import the walk cannot follow inside the project (a missing file, or
 * one outside the root, a symbolic link included) is reported rather than
 * passed, since the rule cannot tell what it loads.
 *
 * Options: `{ root }`, the directory `@/` resolves against.
 */

const isZod = (specifier) => specifier === "zod" || specifier.startsWith("zod/");

const SCRIPT = new Set([".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"]);

/** What TypeScript's bundler resolution tries for a specifier without its own extension. */
const SUFFIXES = ["", ".ts", ".tsx", ".mjs", ".js", "/index.ts", "/index.tsx"];

/** A `.js`-family specifier may name the TypeScript source it is compiled from. */
const SOURCE_OF = { ".js": [".ts", ".tsx"], ".jsx": [".tsx"], ".mjs": [".mts"], ".cjs": [".cts"] };

/** Per file: its modification time and what was read from it, so an editor's long-lived linter stays current. */
const facts = new Map();

const UNRESOLVED = Symbol("unresolved");

/** Whether `path` is a file now; a file removed while the walk runs is simply absent. */
function isFile(path) {
  try {
    return statSync(path).isFile();
  } catch {
    return false;
  }
}

function realPath(path) {
  try {
    return realpathSync(path);
  } catch {
    return null;
  }
}

/**
 * The real path of a local specifier's file; `UNRESOLVED` when it has none
 * inside `root` (which is itself a real path); null for a package.
 */
function resolveLocal(root, from, specifier) {
  let base;
  if (specifier.startsWith("@/")) base = join(root, specifier.slice(2));
  else if (specifier.startsWith(".")) base = resolve(dirname(from), specifier);
  else return null;

  const candidates = SUFFIXES.map((suffix) => base + suffix);
  const extension = extname(base);
  for (const source of SOURCE_OF[extension] ?? []) candidates.push(base.slice(0, -extension.length) + source);
  for (const candidate of candidates) {
    if (!isFile(candidate)) continue;
    const real = realPath(candidate);
    if (real === null) continue;
    if (real !== root && !real.startsWith(root + sep)) return UNRESOLVED;
    return real;
  }
  return UNRESOLVED;
}

const valueElements = (elements) => elements.filter((element) => !element.isTypeOnly);

/** The names a TypeScript import or re-export takes as values; null when it takes only types. */
function namesTaken(statement) {
  if (ts.isImportDeclaration(statement)) {
    const clause = statement.importClause;
    if (!clause) return [];
    if (clause.isTypeOnly) return null;
    const names = clause.name ? ["default"] : [];
    const bindings = clause.namedBindings;
    if (bindings && ts.isNamespaceImport(bindings)) names.push("*");
    if (bindings && ts.isNamedImports(bindings)) {
      const values = valueElements(bindings.elements);
      if (!clause.name && bindings.elements.length > 0 && values.length === 0) return null;
      names.push(...values.map((element) => (element.propertyName ?? element.name).text));
    }
    return names;
  }
  if (statement.isTypeOnly) return null;
  const clause = statement.exportClause;
  if (!clause || ts.isNamespaceExport(clause)) return ["*"];
  const values = valueElements(clause.elements);
  if (clause.elements.length > 0 && values.length === 0) return null;
  return values.map((element) => (element.propertyName ?? element.name).text);
}

/** Members whose `name` is a key, not a read: `{ key: value }`, `key() {}` and the like. */
const KEYED = [
  ts.isPropertyAssignment,
  ts.isMethodDeclaration,
  ts.isPropertyDeclaration,
  ts.isGetAccessorDeclaration,
  ts.isSetAccessorDeclaration,
];

/**
 * The names `node` reads as values. Types (type parameters included), enums,
 * labels, property keys and names after a dot are not reads, and a name the
 * node declares itself (a parameter, a local) is not a read of the module's
 * binding anywhere inside it: stricter than real scoping, so a shadowed
 * adapter never counts as a call.
 */
function valueReads(node) {
  const reads = new Set();
  const declared = new Set();
  const visitExcept = (parent, skipped) =>
    ts.forEachChild(parent, (child) => {
      if (child !== skipped) visit(child);
    });
  const declare = (name) => {
    if (ts.isIdentifier(name)) {
      declared.add(name.text);
      return;
    }
    for (const element of name.elements) {
      if (!ts.isBindingElement(element)) continue;
      declare(element.name);
      if (element.propertyName && ts.isComputedPropertyName(element.propertyName)) visit(element.propertyName);
      if (element.initializer) visit(element.initializer);
    }
  };
  function visit(child) {
    if (
      ts.isTypeNode(child) ||
      ts.isTypeParameterDeclaration(child) ||
      ts.isTypeAliasDeclaration(child) ||
      ts.isInterfaceDeclaration(child) ||
      ts.isEnumDeclaration(child) ||
      ts.isBreakOrContinueStatement(child)
    )
      return;
    if (ts.isLabeledStatement(child)) {
      visit(child.statement);
      return;
    }
    if (ts.isIdentifier(child)) {
      reads.add(child.text);
      return;
    }
    if (ts.isPropertyAccessExpression(child)) {
      visit(child.expression);
      return;
    }
    if (ts.isParameter(child) || ts.isVariableDeclaration(child)) {
      declare(child.name);
      if (child.initializer) visit(child.initializer);
      return;
    }
    if ((ts.isFunctionLike(child) || ts.isClassLike(child)) && child.name && ts.isIdentifier(child.name)) {
      declared.add(child.name.text);
      visitExcept(child, child.name);
      return;
    }
    if (KEYED.some((is) => is(child)) && !ts.isComputedPropertyName(child.name)) {
      visitExcept(child, child.name);
      return;
    }
    if (ts.isJsxAttribute(child)) {
      visitExcept(child, child.name);
      return;
    }
    ts.forEachChild(child, visit);
  }
  visit(node);
  return new Set([...reads].filter((name) => !declared.has(name)));
}

const exported = (node) => ts.getModifiers?.(node)?.some((m) => m.kind === ts.SyntaxKind.ExportKeyword) ?? false;
const exportedDefault = (node) => ts.getModifiers?.(node)?.some((m) => m.kind === ts.SyntaxKind.DefaultKeyword) ?? false;

/**
 * The exported names of `source` that call the browser adapter, directly or
 * through a top-level helper of the same module.
 */
function browserCalls(source, adapterNames) {
  if (adapterNames.size === 0) return new Set();
  const declarations = new Map();
  const exports = new Map();
  for (const statement of source.statements) {
    if (ts.isFunctionDeclaration(statement) && statement.name) {
      declarations.set(statement.name.text, valueReads(statement));
      if (exported(statement)) exports.set(exportedDefault(statement) ? "default" : statement.name.text, statement.name.text);
    } else if (ts.isVariableStatement(statement)) {
      for (const declaration of statement.declarationList.declarations) {
        if (!ts.isIdentifier(declaration.name)) continue;
        declarations.set(declaration.name.text, valueReads(declaration));
        if (exported(statement)) exports.set(declaration.name.text, declaration.name.text);
      }
    } else if (ts.isExportDeclaration(statement) && !statement.moduleSpecifier && statement.exportClause) {
      if (!ts.isNamedExports(statement.exportClause)) continue;
      for (const element of statement.exportClause.elements)
        exports.set(element.name.text, (element.propertyName ?? element.name).text);
    }
  }

  const calling = new Set();
  for (let grew = true; grew; ) {
    grew = false;
    for (const [name, uses] of declarations) {
      if (calling.has(name)) continue;
      if ([...uses].some((use) => adapterNames.has(use) || calling.has(use))) {
        calling.add(name);
        grew = true;
      }
    }
  }
  return new Set([...exports].filter(([, local]) => calling.has(local)).map(([name]) => name));
}

/**
 * A module's directive, its value edges (specifier and names taken) and its
 * exported browser calls; null when the file can no longer be read.
 */
function readFacts(root, path) {
  let mtimeMs;
  try {
    mtimeMs = statSync(path).mtimeMs;
  } catch {
    facts.delete(path);
    return null;
  }
  const known = facts.get(path);
  if (known && known.mtimeMs === mtimeMs) return known.value;

  const value = { directive: null, edges: [], calls: new Set() };
  if (SCRIPT.has(extname(path))) {
    let text;
    try {
      text = readFileSync(path, "utf8");
    } catch {
      facts.delete(path);
      return null;
    }
    const source = ts.createSourceFile(path, text, ts.ScriptTarget.Latest, true);
    for (const statement of source.statements) {
      if (!ts.isExpressionStatement(statement) || !ts.isStringLiteral(statement.expression)) break;
      if (statement.expression.text === "use client" || statement.expression.text === "use server")
        value.directive = statement.expression.text;
    }
    const adapter = join(root, "lib/api/browser.ts");
    const adapterNames = new Set();
    for (const statement of source.statements) {
      const isImport = ts.isImportDeclaration(statement);
      if (!isImport && !(ts.isExportDeclaration(statement) && statement.moduleSpecifier)) continue;
      const names = namesTaken(statement);
      if (names === null) continue;
      const specifier = statement.moduleSpecifier.text;
      value.edges.push({ specifier, names });
      const bindings = isImport ? statement.importClause?.namedBindings : undefined;
      if (bindings && ts.isNamedImports(bindings) && resolveLocal(root, path, specifier) === adapter)
        for (const element of valueElements(bindings.elements)) adapterNames.add(element.name.text);
    }
    value.calls = browserCalls(source, adapterNames);
  }
  facts.set(path, { mtimeMs, value });
  return value;
}

/**
 * The walk from an import of `path` taking `names`: the files that lead to zod
 * (`zod: true`) or to a local import that cannot be followed (`unresolved`),
 * or null when it reaches neither.
 */
function walk(root, path, names, seen) {
  const known = readFacts(root, path);
  if (known === null || known.directive !== null) return null;
  if (names.some((name) => known.calls.has(name))) return null;
  if (seen.has(path)) return null;
  seen.add(path);

  for (const { specifier, names: taken } of known.edges) {
    if (isZod(specifier)) return { files: [path], zod: true };
    const target = resolveLocal(root, path, specifier);
    if (target === UNRESOLVED) return { files: [path], unresolved: specifier };
    if (target === null) continue;
    const found = walk(root, target, taken, seen);
    if (found) return { ...found, files: [path, ...found.files] };
  }
  return null;
}

/** The names an ESTree import or re-export takes as values; null when it takes only types. */
function estreeNames(node) {
  if (node.importKind === "type" || node.exportKind === "type") return null;
  if (node.type === "ExportAllDeclaration") return ["*"];
  const specifiers = node.specifiers ?? [];
  const values = specifiers.filter((s) => s.importKind !== "type" && s.exportKind !== "type");
  if (specifiers.length > 0 && values.length === 0) return null;
  return values.map((s) => {
    if (s.type === "ImportDefaultSpecifier") return "default";
    if (s.type === "ImportNamespaceSpecifier") return "*";
    const name = s.type === "ImportSpecifier" ? s.imported : s.local;
    return name.type === "Identifier" ? name.name : name.value;
  });
}

/** @type {import("eslint").Rule.RuleModule} */
export const noSchemaInClient = {
  meta: {
    type: "problem",
    docs: { description: "Forbids a client file's value imports that load zod outside a browser call." },
    schema: [{ type: "object", properties: { root: { type: "string" } }, required: ["root"], additionalProperties: false }],
    messages: {
      zod: "{{path}} loads zod into this client bundle. Take the constant from the area's *-terms module (see lib/api/content-terms.ts).",
      unresolved: "{{path}} cannot be followed inside the project, so whether it loads zod is unknown.",
    },
  },
  create(context) {
    const program = context.sourceCode.ast;
    const client = program.body.some((statement) => statement.directive === "use client");
    if (!client) return {};

    const root = realpathSync(context.options[0].root);
    const show = (file) => relative(root, file);
    const check = (node) => {
      if (!node.source) return;
      const names = estreeNames(node);
      if (names === null) return;
      const specifier = node.source.value;
      if (isZod(specifier)) {
        context.report({ node, messageId: "zod", data: { path: specifier } });
        return;
      }
      const target = resolveLocal(root, context.filename, specifier);
      if (target === null) return;
      if (target === UNRESOLVED) {
        context.report({ node, messageId: "unresolved", data: { path: specifier } });
        return;
      }
      const found = walk(root, target, names, new Set());
      if (!found) return;
      const path = found.files.map(show);
      if (found.unresolved) path.push(found.unresolved);
      context.report({ node, messageId: found.zod ? "zod" : "unresolved", data: { path: path.join(" -> ") } });
    };
    return { ImportDeclaration: check, ExportNamedDeclaration: check, ExportAllDeclaration: check };
  },
};
