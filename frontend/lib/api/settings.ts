import { z } from "zod";

/**
 * What the installation calls itself.
 *
 * The keys are the API's, spelled once here. They are dotted strings rather
 * than an object because that is what the table holds — a setting is a row,
 * so that adding one is data rather than a release.
 */
export const SETTING_KEYS = {
  name: "installation.name",
  contact: "installation.contact_email",
  logo: "installation.logo",
} as const;

/**
 * A value may be missing for a key this build knows and the server does not,
 * or the other way round, so every read is defaulted rather than required.
 * Neither direction is worth failing a page over: the worst case is a heading
 * that falls back, and a page that refuses to render is worse than that.
 */
export const settingsSchema = z
  .object({ values: z.record(z.string(), z.string()).default({}) })
  .transform(({ values }) => ({
    name: values[SETTING_KEYS.name] ?? "",
    contact: values[SETTING_KEYS.contact] ?? "",
    logo: values[SETTING_KEYS.logo] ?? "",
  }));

export type Settings = z.infer<typeof settingsSchema>;
