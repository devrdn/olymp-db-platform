import { serverRequest } from "@/lib/api/server";
import { mailStatusSchema } from "@/lib/api/settings";

/**
 * Whether the /users screens offer to email sign-in details (the mail status
 * the Installation screen reads), and the form field that carries the choice.
 * Server-side only: `SendByEmailOption` in `email-delivery.tsx` renders the
 * field.
 */

/** True only for a ticked `send_by_email` box; an unticked one submits nothing. */
export function sendByEmail(form: FormData): boolean {
  return form.get("send_by_email") === "on";
}

/**
 * Whether mail is on, from `/settings/all`. Any failure reads as off: that
 * read needs settings.manage, which an account manager may lack, and without
 * the option the screens still hand every password over as before. The server
 * refuses the option with `mail_off` regardless.
 */
export async function readMailEnabled(): Promise<boolean> {
  return serverRequest("/settings/all")
    .then((payload) => mailStatusSchema.parse(payload).enabled)
    .catch(() => false);
}
