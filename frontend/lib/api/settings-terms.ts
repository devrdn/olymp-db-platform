/**
 * The installation's setting keys and image addresses, apart from the wire
 * schemas (see content-terms.ts for why).
 */
export const SETTING_KEYS = {
  name: "installation.name",
  contact: "installation.contact_email",
} as const;

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
