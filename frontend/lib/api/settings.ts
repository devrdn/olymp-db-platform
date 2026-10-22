import { z } from "zod";

import { SETTING_KEYS, type ImageKind } from "./settings-terms";

export {
  FEEDBACK_URL_MAX,
  IMAGE_KINDS,
  MAIL_LANGUAGES,
  SETTING_KEYS,
  feedbackHref,
  imageHref,
  type ImageKind,
} from "./settings-terms";

/**
 * The installation's name, contact, feedback address, mail language and images. Settings
 * are rows keyed by dotted strings, so adding one is data rather than a
 * release.
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
    feedback: values[SETTING_KEYS.feedback] ?? "",
    mailLanguage: values[SETTING_KEYS.mailLanguage] ?? "",
    images: images as Partial<Record<ImageKind, string>>,
  }));

export type Settings = z.infer<typeof settingsSchema>;

/**
 * Whether the installation sends email, and the address it sends as. Only the
 * administrator's read (`/settings/all`) carries it; absent reads as off.
 */
export const mailStatusSchema = z
  .object({
    mail: z.object({ enabled: z.boolean(), from: z.string() }).default({ enabled: false, from: "" }),
  })
  .transform(({ mail }) => mail);

export type MailStatus = z.infer<typeof mailStatusSchema>;
