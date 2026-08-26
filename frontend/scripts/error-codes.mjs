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
const DICTIONARY = "lib/i18n/dictionaries/en.ts";

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
 */
function serverCodes() {
  const found = new Set();
  const call = /\bError\(\s*w,\s*r,[^,]+,\s*"([a-z_]+)"/g;

  for (const file of goFiles(BACKEND)) {
    const source = readFileSync(file, "utf8");
    for (const match of source.matchAll(call)) found.add(match[1]);
  }
  return found;
}

function dictionaryCodes() {
  const source = readFileSync(DICTIONARY, "utf8");
  const errors = source.split("errors:")[1];
  if (!errors) throw new Error(`No errors block in ${DICTIONARY}`);

  return new Set([...errors.matchAll(/^\s{4}([a-z_]+):/gm)].map((m) => m[1]));
}

const server = serverCodes();
const dictionary = dictionaryCodes();

const missing = [...server].filter((code) => !dictionary.has(code)).sort();
const stale = [...dictionary].filter((code) => !server.has(code) && !CLIENT_ONLY.has(code)).sort();

for (const code of missing) {
  console.error(`missing   ${code}  — the API returns it and no locale has a message for it`);
}
for (const code of stale) {
  console.error(`unused    ${code}  — no longer returned by the API`);
}

if (missing.length || stale.length) {
  console.error(`\n${missing.length} missing, ${stale.length} unused.`);
  process.exit(1);
}

console.log(`Every one of the ${server.size} codes the API returns has a message.`);
