#!/usr/bin/env node
/**
 * The error dictionary, checked against the vocabulary the API actually speaks.
 *
 * The specification promises that every machine code the server
 * returns has a translated message, and that the English the server sends
 * alongside it never reaches a person. Nothing enforced the first half: a code
 * added on the Go side simply fell through to the generic sentence, and the
 * only symptom was a user reading "Something went wrong" where a real reason
 * existed. That is exactly the kind of drift review does not catch, because
 * neither side looks wrong on its own.
 *
 * Run from the frontend directory with the repository checked out.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";

const BACKEND = "../backend/internal";
const DICTIONARIES = "lib/i18n/dictionaries";

/** Codes this layer invents for failures that never reached the API. */
const CLIENT_ONLY = new Set(["fallback", "unreachable", "password_mismatch"]);

function goFiles(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) out.push(...goFiles(path));
    else if (entry.endsWith(".go") && !entry.endsWith("_test.go")) out.push(path);
  }
  return out;
}

/**
 * Every code the service can put on the wire.
 *
 * Matched on the call rather than on a constant list, because the codes are
 * written inline at the call site — including inside `platform/httpx` itself,
 * where the helper is called without its package name.
 *
 * Two shapes, because there are two ways a code reaches the wire. Most go
 * through the httpx.Error helper. A handler that has to send more than the
 * error object — the publish gate returns the list of what is missing
 * alongside it — writes the envelope itself, and that form was invisible here:
 * `not_publishable` shipped with no message and this check reported success,
 * which is precisely the drift it exists to catch.
 */
function serverCodes() {
  const found = new Set();
  const shapes = [
    /\bError\(\s*w,\s*r,[^,]+,\s*"([a-z_]+)"/g,
    /"code":\s*"([a-z_]+)"/g,
  ];

  for (const file of goFiles(BACKEND)) {
    const source = readFileSync(file, "utf8");
    for (const shape of shapes) {
      for (const match of source.matchAll(shape)) found.add(match[1]);
    }
  }
  return found;
}

function dictionaryCodes(file) {
  const source = readFileSync(join(DICTIONARIES, file), "utf8");
  const errors = source.split("errors:")[1];
  if (!errors) throw new Error(`No errors block in ${file}`);

  return new Set([...errors.matchAll(/^\s{4}([a-z_]+):/gm)].map((m) => m[1]));
}

const server = serverCodes();
// Every locale, not only English. A code translated in one language and
// forgotten in another is the same defect seen by fewer people, and checking
// the one file the author had open is how it stays hidden.
const locales = readdirSync(DICTIONARIES).filter((file) => file.endsWith(".ts")).sort();

let failures = 0;
for (const locale of locales) {
  const dictionary = dictionaryCodes(locale);

  const missing = [...server].filter((code) => !dictionary.has(code)).sort();
  const stale = [...dictionary].filter((code) => !server.has(code) && !CLIENT_ONLY.has(code)).sort();

  for (const code of missing) {
    console.error(`missing   ${locale}  ${code}  — the API returns it and this locale has no message`);
  }
  for (const code of stale) {
    console.error(`unused    ${locale}  ${code}  — no longer returned by the API`);
  }
  failures += missing.length + stale.length;
}

if (failures) {
  console.error(`\n${failures} problem(s) across ${locales.length} locales.`);
  process.exit(1);
}

console.log(`Every one of the ${server.size} codes the API returns has a message in all ${locales.length} locales.`);
