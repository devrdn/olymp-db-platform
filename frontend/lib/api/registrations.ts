import { z } from "zod";

import type { Registration } from "./registrations-terms";

/**
 * The wire shapes of the event registration endpoints. snake_case is mapped to
 * camelCase once, here.
 */

/** `POST /public/registrations` for an email seen for the first time. */
const createdSchema = z
  .object({
    status: z.literal("created"),
    code: z.string(),
    first_name: z.string(),
    last_name: z.string(),
    email: z.string(),
    account: z.enum(["created", "existing"]),
    // Present only when `account` is "created".
    login: z.string().optional(),
    one_time_password: z.string().optional(),
    email_sent: z.boolean(),
  })
  .transform((raw) => ({
    status: raw.status,
    code: raw.code,
    firstName: raw.first_name,
    lastName: raw.last_name,
    email: raw.email,
    account: raw.account,
    login: raw.login,
    oneTimePassword: raw.one_time_password,
    emailSent: raw.email_sent,
  }));

/**
 * Mail is on: the code and the sign-in details went by email, and the answer
 * is the same for a new address, a repeat and an address with an account.
 */
const acceptedSchema = z
  .object({
    status: z.literal("accepted"),
    email: z.string(),
    email_sent: z.literal(true),
  })
  .transform((raw) => ({ status: raw.status, email: raw.email }));

/** Mail is off: a repeat, whose code is never shown, so the page cannot leak it. */
const repeatSchema = z
  .object({
    status: z.literal("existing"),
    email: z.string(),
    email_sent: z.boolean(),
  })
  .transform((raw) => ({ status: raw.status, email: raw.email, emailSent: raw.email_sent }));

export const registrationOutcomeSchema = z.union([acceptedSchema, createdSchema, repeatSchema]);
export type RegistrationOutcome = z.infer<typeof registrationOutcomeSchema>;

export const registrationSchema = z
  .object({
    id: z.string(),
    first_name: z.string(),
    last_name: z.string(),
    email: z.string(),
    code: z.string(),
    user_id: z.string().nullish(),
    created_at: z.string(),
    checked_in_at: z.string().nullish(),
  })
  .transform(
    (raw): Registration => ({
      id: raw.id,
      firstName: raw.first_name,
      lastName: raw.last_name,
      email: raw.email,
      code: raw.code,
      userId: raw.user_id ?? undefined,
      createdAt: raw.created_at,
      checkedInAt: raw.checked_in_at ?? undefined,
    }),
  );

export const registrationListSchema = z
  .object({
    registrations: z.array(registrationSchema),
    total: z.number(),
    checked_in: z.number(),
  })
  .transform((raw) => ({
    registrations: raw.registrations,
    total: raw.total,
    checkedIn: raw.checked_in,
  }));
