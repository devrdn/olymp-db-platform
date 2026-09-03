import { z } from "zod";

import { SETTING_KEYS, type ImageKind } from "./settings-terms";

export { IMAGE_KINDS, SETTING_KEYS, imageHref, type ImageKind } from "./settings-terms";

/**
 * What the installation calls itself, and what it puts on itself.
 *
 * The keys are the API's, spelled once here. They are dotted strings rather
 * than an object because that is what the table holds — a setting is a row, so
 * that adding one is data rather than a release.
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

