#!/usr/bin/env node
/**
 * The error dictionary, checked against the vocabulary the API publishes.
 *
 * The specification promises that every machine code the server returns has a
 * translated message, and that the English the server sends alongside it never
 * reaches a person. Without this check a code added on the server falls through
 * to the generic sentence, and the only symptom is a user reading "Something
 * went wrong" where a real reason exists. Review does not catch that drift,
 * because neither side looks wrong on its own.
 *
 * The codes come from the contract the server generates
 * (docs/api/error-codes.json), which is exact by construction: a code that is
 * not declared cannot be sent. Nothing here knows that the server is written
 * in Go, so if the two ever live in separate repositories, only CONTRACT
 * changes: from a path to wherever the published file is fetched from.
 *
 * The dictionaries are imported, not read as text, so the check sees the keys
 * the application sees whatever the file's layout. They are TypeScript; the npm
 * script runs this with Node's type stripping, which is enough because they
 * carry only erasable syntax (`import type`, `satisfies`). The script silences
 * two warnings that say nothing about the check: ExperimentalWarning, which
 * type stripping prints on every run on the Node versions that still flag it,
 * and MODULE_TYPELESS_PACKAGE_JSON, which Node prints once per file because
 * package.json has no `"type"` and it re-parsed the `.ts` as a module. The
 * re-parse is the behaviour wanted, and adding `"type": "module"` would change
 * how every other script and config is loaded.
 */

import { readdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { pathToFileURL } from "node:url";

const CONTRACT = "../docs/api/error-codes.json";
const DICTIONARIES = "lib/i18n/dictionaries";

/** Codes this layer invents for failures that never reached the API. */
const CLIENT_ONLY = new Set(["fallback", "unreachable", "password_mismatch"]);

function serverCodes() {
  let contract;
  try {
    contract = JSON.parse(readFileSync(CONTRACT, "utf8"));
  } catch (cause) {
    throw new Error(
      `Cannot read ${CONTRACT}. Regenerate it with \`make api-contract\`.`,
      { cause },
    );
  }
  if (!Array.isArray(contract.codes) || contract.codes.length === 0) {
    throw new Error(`${CONTRACT} lists no codes; regenerate it with \`make api-contract\`.`);
  }
  return new Set(contract.codes.map((entry) => entry.code));
}

async function dictionaryCodes(file) {
  const { default: dictionary } = await import(pathToFileURL(resolve(DICTIONARIES, file)).href);
  const errors = dictionary?.errors;
  if (typeof errors !== "object" || errors === null || Object.keys(errors).length === 0) {
    throw new Error(`No errors block in ${file}`);
  }
  // An empty sentence would pass a key check and show a person nothing.
  const blank = Object.entries(errors).filter(([, message]) => typeof message !== "string" || message.trim() === "");
  if (blank.length > 0) {
    throw new Error(`${file} has an empty message for: ${blank.map(([code]) => code).join(", ")}`);
  }

  return new Set(Object.keys(errors));
}

const server = serverCodes();
// Every locale, not only English. A code translated in one language and
// forgotten in another is the same defect seen by fewer people, and checking
// the one file the author had open is how it stays hidden.
const locales = readdirSync(DICTIONARIES).filter((file) => file.endsWith(".ts")).sort();
if (locales.length === 0) throw new Error(`No dictionaries found in ${DICTIONARIES}.`);

let failures = 0;
for (const locale of locales) {
  const dictionary = await dictionaryCodes(locale);

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

console.log(`Every one of the ${server.size} codes the API publishes has a message in all ${locales.length} locales.`);
