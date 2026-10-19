/**
 * The installation's setting keys and image addresses, apart from the wire
 * schemas (see content-terms.ts for why).
 */
export const SETTING_KEYS = {
  name: "installation.name",
  contact: "installation.contact_email",
  feedback: "installation.feedback_url",
} as const;

/** The API's bound on the feedback address (`maxFeedbackURLLength`). */
export const FEEDBACK_URL_MAX = 500;

/**
 * The feedback address as a link target, or null when the API would refuse
 * it: an absolute https address with a host and no credentials, no spaces,
 * within the bound. Checked again before drawing a link, so a stored value
 * the rule refuses never becomes an `href`.
 */
export function feedbackHref(value: string): string | null {
  if (value.length === 0 || value.length > FEEDBACK_URL_MAX) return null;
  // The URL parser forgives what the API's does not: a missing `//`, a
  // surrounding space, a backslash for a slash.
  if (!/^https:\/\//i.test(value) || /[\s\p{Cc}\\]/u.test(value)) return null;
  try {
    const url = new URL(value);
    if (url.protocol !== "https:" || url.hostname === "" || url.username !== "" || url.password !== "") {
      return null;
    }
  } catch {
    return null;
  }
  return value;
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
