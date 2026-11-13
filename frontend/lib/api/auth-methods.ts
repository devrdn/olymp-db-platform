import { z } from "zod";

import { PROVIDER_IDS, type ProviderId } from "@/lib/auth/external";

import { failureCode } from "./client";
import { serverRequest } from "./server";

/** The ways in this installation offers, as `GET /auth/methods` says. */
export const authMethodsSchema = z
  .object({
    password_signup: z.boolean(),
    password_reset: z.boolean(),
    microsoft: z.boolean(),
    trusted_email_domain: z.string(),
    providers: z.array(z.object({ id: z.string().max(32) })).max(16),
  })
  .transform((body) => ({
    passwordSignup: body.password_signup,
    passwordReset: body.password_reset,
    microsoft: body.microsoft,
    trustedEmailDomain: body.trusted_email_domain,
    // The fixed order, whatever the API sends; an id this release does not know is dropped.
    providers: PROVIDER_IDS.filter((id) => body.providers.some((p) => p.id === id)) as ProviderId[],
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
