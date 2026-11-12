import { z } from "zod";

import { failureCode } from "./client";
import { serverRequest } from "./server";

/** The ways in this installation offers, as `GET /auth/methods` says. */
export const authMethodsSchema = z
  .object({
    password_signup: z.boolean(),
    password_reset: z.boolean(),
    microsoft: z.boolean(),
    trusted_email_domain: z.string(),
  })
  .transform((body) => ({
    passwordSignup: body.password_signup,
    passwordReset: body.password_reset,
    microsoft: body.microsoft,
    trustedEmailDomain: body.trusted_email_domain,
  }));

export type AuthMethods = z.infer<typeof authMethodsSchema>;

/**
 * The ways in, or null when the API could not say. Never throws: the pages
 * that ask only offer more, and must keep working without the answer.
 */
export async function fetchAuthMethods(): Promise<AuthMethods | null> {
  try {
    return authMethodsSchema.parse(await serverRequest("/auth/methods"));
  } catch (error) {
    console.warn(`auth methods unavailable: ${failureCode(error)}`);
    return null;
  }
}
