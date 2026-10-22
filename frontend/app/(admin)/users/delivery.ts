import { unstable_rethrow } from "next/navigation";

import { mailStatusSchema } from "@/lib/api/accounts";
import { serverRequest } from "@/lib/api/server";

import type { MailOffer } from "./email-delivery";

/**
 * Whether the /users screens offer to email sign-in details, and the form
 * field that carries the choice. Server-side only: `SendByEmailOption` in
 * `email-delivery.tsx` renders the field.
 */

/** True only for a ticked `send_by_email` box; an unticked one submits nothing. */
export function sendByEmail(form: FormData): boolean {
  return form.get("send_by_email") === "on";
}

const MAIL_STATUS = "/users/mail-status";

/**
 * The mail status as the screens offer it. A failed read is `unknown`, never
 * off: the screens say they could not check, and the failure is logged, since
 * the page still renders.
 */
export async function readMailOffer(): Promise<MailOffer> {
  try {
    const status = mailStatusSchema.parse(await serverRequest(MAIL_STATUS));
    return status.enabled ? { status: "on", perMinute: status.perMinute } : { status: "off" };
  } catch (error: unknown) {
    unstable_rethrow(error);
    console.error("reading %s for the accounts screens failed", MAIL_STATUS, error);
    return { status: "unknown" };
  }
}
