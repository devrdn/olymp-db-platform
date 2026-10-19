/**
 * The installation's setting keys and image addresses, apart from the wire
 * schemas (see content-terms.ts for why).
 */
export const SETTING_KEYS = {
  name: "installation.name",
  contact: "installation.contact_email",
  feedback: "installation.feedback_url",
} as const;

/** The API's bound on the feedback address, in bytes (`maxFeedbackURLLength`). */
export const FEEDBACK_URL_MAX = 500;

/** The API's `feedbackAuthority`: DNS labels, the last starting with a letter, and an optional port. */
const FEEDBACK_AUTHORITY = /^((?:[A-Za-z0-9-]+\.)*[A-Za-z][A-Za-z0-9-]*)(?::([0-9]{1,5}))?$/;

/**
 * The feedback address as a link target, or null when the API would refuse
 * it. The API's rule (`optionalFeedbackURL`) written out the same way rather
 * than left to `new URL`, which forgives what Go's parser does not; both are
 * tested against one table. Printable ASCII only, so `length` counts the same
 * bytes the API does. Checked again before drawing a link, so a stored value
 * the rule refuses never becomes an `href`.
 */
export function feedbackHref(value: string): string | null {
  if (value.length === 0 || value.length > FEEDBACK_URL_MAX) return null;
  if (!/^[\x21-\x5B\x5D-\x7E]+$/.test(value)) return null;
  if (!/^https:\/\//i.test(value)) return null;

  const rest = value.slice("https://".length);
  const authority = rest.split(/[/?#]/, 1)[0];
  const match = FEEDBACK_AUTHORITY.exec(authority);
  if (!match) return null;
  if (match[1].split(".").some((label) => label.toLowerCase().startsWith("xn--"))) return null;
  if (match[2] !== undefined) {
    const port = Number(match[2]);
    if (port < 1 || port > 65535) return null;
  }
  if (/%(?![0-9A-Fa-f]{2})/.test(value)) return null;

  return URL.canParse(value) ? value : null;
}

/** The pictures an installation may replace. */
export const IMAGE_KINDS = ["logo", "icon", "favicon"] as const;
export type ImageKind = (typeof IMAGE_KINDS)[number];

/**
 * A stored picture's address. The hash in the query changes with the picture,
 * which lets the response be cached forever.
 */
export function imageHref(kind: ImageKind, hash: string): string {
  return `/api/v1/settings/images/${kind}?v=${hash}`;
}
