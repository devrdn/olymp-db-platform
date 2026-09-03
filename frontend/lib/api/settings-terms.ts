/**
 * What the installation's settings are called, and where a stored picture
 * lives.
 *
 * Apart from the schema next door for the reason accounts-terms gives: a
 * module that calls `z.object()` at load cannot be dropped by a bundler, so a
 * client component importing one helper from it ships the whole parser.
 */
export const SETTING_KEYS = {
  name: "installation.name",
  contact: "installation.contact_email",
} as const;

/** The pictures an installation may replace, and what each is for. */
export const IMAGE_KINDS = ["logo", "icon", "favicon"] as const;
export type ImageKind = (typeof IMAGE_KINDS)[number];

/**
 * A value may be missing for a key this build knows and the server does not,
 * or the other way round, so every read is defaulted rather than required.
 * Neither direction is worth failing a page over: the worst case is a heading
 * that falls back, and a page that refuses to render is worse than that.
 */

/**
 * Where a stored picture lives, with its hash in the query.
 *
 * The hash is what makes the address change when the picture does, which is
 * what lets the response be cached forever: a browser holding the old logo is
 * holding it under an address nothing links to any more.
 */
export function imageHref(kind: ImageKind, hash: string): string {
  return `/api/v1/settings/images/${kind}?v=${hash}`;
}
