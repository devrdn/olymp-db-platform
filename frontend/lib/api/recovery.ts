import { z } from "zod";

/**
 * The forgotten-password flow: a request for a link, answered alike for
 * every account and for none, and the reset the link opens.
 */

/** The one answer every admitted request for a link gets. */
export const forgotAnswerSchema = z.object({ status: z.literal("link_sent") });

/**
 * `recovery.TokenLength`: 32 random bytes as unpadded base64url. Anything
 * else cannot be a link's token, so it is refused without asking the API.
 */
const RESET_TOKEN = /^[A-Za-z0-9_-]{43}$/;

export function isResetToken(token: string): boolean {
  return RESET_TOKEN.test(token);
}
