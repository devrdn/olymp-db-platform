import { z } from "zod";

/**
 * What the installation calls itself, and what it puts on itself.
 *
 * The keys are the API's, spelled once here. They are dotted strings rather
 * than an object because that is what the table holds — a setting is a row, so
 * that adding one is data rather than a release.
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
export const settingsSchema = z
  .object({
    values: z.record(z.string(), z.string()).default({}),
    /** Slot to content hash, for the ones that hold a picture. */
    images: z.record(z.string(), z.string()).default({}),
  })
  .transform(({ values, images }) => ({
    name: values[SETTING_KEYS.name] ?? "",
    contact: values[SETTING_KEYS.contact] ?? "",
    images: images as Partial<Record<ImageKind, string>>,
  }));

export type Settings = z.infer<typeof settingsSchema>;

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
