#!/usr/bin/env node
/**
 * The error dictionary, checked against the vocabulary the API publishes.
 *
 * The specification promises that every machine code the server returns has a
 * translated message, and that the English the server sends alongside it never
 * reaches a person. Nothing enforced the first half: a code added on the server
 * fell through to the generic sentence, and the only symptom was a user reading
 * "Something went wrong" where a real reason existed. That is exactly the kind
 * of drift review does not catch, because neither side looks wrong on its own.
 *
 * This used to read the server's own source and recover the codes with a
 * regular expression. It was both a dependency on another language's layout and
 * unsound: a code written anywhere but the expected call shape was invisible,
 * and `not_publishable` shipped with no message in any language while this
 * check reported success.
 *
 * It now reads the contract the server generates (docs/api/error-codes.json),
 * which is exact by construction — a code that is not declared cannot be sent.
 * Nothing here knows that the server is written in Go, so if the two ever live
 * in separate repositories, only CONTRACT changes: from a path to wherever the
 * published file is fetched from.
 */

import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";

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

console.log(`Every one of the ${server.size} codes the API publishes has a message in all ${locales.length} locales.`);
