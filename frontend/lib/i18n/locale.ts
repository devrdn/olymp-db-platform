/**
 * Mirrors platform/i18n.Match on the server, so "which language did they get,
 * and why" has one answer on both sides of the wire (spec section 8).
 *
 * A region refines a language rather than replacing it, so matching works in
 * both directions: a request for ro-MD accepts an available ro, and a request
 * for ro accepts an available ro-MD.
 */

function normalize(tag: string): string {
  return tag.trim().toLowerCase();
}

function baseOf(tag: string): string {
  return normalize(tag).split("-")[0];
}

export function matchLocale(preferred: string[], available: string[], fallback: string): string {
  if (available.length === 0) return "";

  for (const want of preferred) {
    const wanted = normalize(want);
    // "*" means "anything", and the fallback is the most sensible reading of it.
    if (wanted === "" || wanted === "*") break;

    const exact = available.find((code) => normalize(code) === wanted);
    if (exact) return exact;

    const sameLanguage = available.find((code) => baseOf(code) === baseOf(wanted));
    if (sameLanguage) return sameLanguage;
  }

  const fallbackHit = available.find((code) => normalize(code) === normalize(fallback));
  return fallbackHit ?? available[0];
}
